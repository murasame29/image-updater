package model

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewImageManifest(t *testing.T) {
	labels := ImageLabels{
		Source:     "https://github.com/example-org/example-app",
		Revision:   "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678",
		Created:    "2026-08-10T00:00:00Z",
		BuildURL:   "https://github.com/example-org/example-app/actions/runs/1",
		PRNumber:   "42",
		PRAuthor:   "alice",
		PRTitle:    "feat: something",
		BuildRef:   "refs/heads/main",
		BuildEvent: "push",
		BuildActor: "bob",
	}

	manifest := NewImageManifest("registry/app", labels)

	assert.Equal(t, "registry/app", manifest.Image)
	assert.Equal(t, "https://github.com/example-org/example-app", manifest.GitHubRepo)
	assert.Equal(t, "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678", manifest.GitSHA)
	assert.Equal(t, "https://github.com/example-org/example-app/commit/a1b2c3d4e5f60718293a4b5c6d7e8f9012345678", manifest.CommitURL)
	assert.Equal(t, "2026-08-10T00:00:00Z", manifest.BuiltAt)
	assert.Equal(t, "https://github.com/example-org/example-app/actions/runs/1", manifest.BuildRunURL)
}

func TestNewImageManifestWithoutLabels(t *testing.T) {
	manifest := NewImageManifest("registry/app", ImageLabels{})

	assert.Equal(t, "registry/app", manifest.Image)
	assert.Empty(t, manifest.GitHubRepo)
	assert.Empty(t, manifest.CommitURL)
	assert.Empty(t, manifest.BuiltAt)
}

func TestNewImageManifestOmitsCommitURLWithoutRevision(t *testing.T) {
	manifest := NewImageManifest("registry/app", ImageLabels{Source: "https://github.com/example-org/example-app"})

	assert.Empty(t, manifest.CommitURL)
}

func TestImageManifestCommentRoundTrip(t *testing.T) {
	var document ImageManifestDocument
	document.Upsert(ImageManifest{Image: "registry/b", GitSHA: "2222"})
	document.Upsert(ImageManifest{Image: "registry/a", GitSHA: "1111"})

	lines, err := RenderImageManifestComment(document)
	require.NoError(t, err)

	assert.True(t, IsImageManifestBegin(lines[0]))
	assert.True(t, IsImageManifestEnd(lines[len(lines)-1]))
	for _, line := range lines {
		assert.True(t, strings.HasPrefix(line, "#"), "every rendered line must be a comment: %q", line)
	}

	// インデント付きで読み戻せること
	indented := make([]string, 0, len(lines))
	for _, line := range lines {
		indented = append(indented, "  "+line)
	}

	parsed, err := ParseImageManifestComment(indented)
	require.NoError(t, err)

	assert.Equal(t, ImageManifestSchemaVersion, parsed.SchemaVersion)
	assert.Equal(t, ImageManifestGenerator, parsed.Generator)
	require.Len(t, parsed.Images, 2)
	assert.Equal(t, "registry/a", parsed.Images[0].Image)
	assert.Equal(t, "registry/b", parsed.Images[1].Image)
}

func TestRenderImageManifestCommentOmitsEmptyFields(t *testing.T) {
	var document ImageManifestDocument
	document.Upsert(ImageManifest{Image: "registry/app"})

	lines, err := RenderImageManifestComment(document)
	require.NoError(t, err)

	rendered := strings.Join(lines, "\n")
	assert.Contains(t, rendered, "#   - image: registry/app")
	for _, key := range []string{"github_repo", "git_sha", "commit_url", "built_at", "build_run_url", "extra"} {
		assert.NotContains(t, rendered, key)
	}
}

func TestImageManifestDocumentUpsert(t *testing.T) {
	tests := []struct {
		name      string
		manifests []ImageManifest
		expected  []ImageManifest
	}{
		{
			name: "同じイメージのエントリは置き換える",
			manifests: []ImageManifest{
				{Image: "registry/app", GitSHA: "1111"},
				{Image: "registry/app", GitSHA: "2222"},
			},
			expected: []ImageManifest{
				{Image: "registry/app", GitSHA: "2222"},
			},
		},
		{
			name: "異なるイメージは別エントリとして保持し、イメージ名順に並べる",
			manifests: []ImageManifest{
				{Image: "registry/b", GitSHA: "2222"},
				{Image: "registry/a", GitSHA: "1111"},
			},
			expected: []ImageManifest{
				{Image: "registry/a", GitSHA: "1111"},
				{Image: "registry/b", GitSHA: "2222"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var document ImageManifestDocument
			for _, manifest := range tt.manifests {
				document.Upsert(manifest)
			}
			assert.Equal(t, tt.expected, document.Images)
		})
	}
}

func TestParseImageManifestCommentIgnoresForeignComments(t *testing.T) {
	parsed, err := ParseImageManifestComment([]string{
		"# " + ImageManifestBeginMarker,
		"# schema_version: v1",
		"# images:",
		"#   - image: registry/app",
		"#     git_sha: 1111",
		"# " + ImageManifestEndMarker,
	})
	require.NoError(t, err)

	require.Len(t, parsed.Images, 1)
	assert.Equal(t, "registry/app", parsed.Images[0].Image)
	assert.Equal(t, "1111", parsed.Images[0].GitSHA)
}

func TestNewExtraLabels(t *testing.T) {
	tests := []struct {
		name     string
		labels   map[string]string
		expected map[string]string
	}{
		{
			name: "prefix 付きのラベルだけ抽出する",
			labels: map[string]string{
				LabelSource:                      "https://github.com/example-org/example-app",
				LabelExtraPrefix + "team":        "platform",
				LabelExtraPrefix + "slack":       "#platform-alerts",
				"com.example.unrelated":          "ignored",
				LabelExtraPrefix + "empty":       "",
				LabelExtraPrefix + "!!!":         "dropped",
				LabelExtraPrefix + "runbook.url": "https://example.com/runbook",
			},
			expected: map[string]string{
				"team":        "platform",
				"slack":       "#platform-alerts",
				"runbook_url": "https://example.com/runbook",
			},
		},
		{
			name:     "prefix 付きのラベルがない場合は nil",
			labels:   map[string]string{LabelSource: "https://github.com/example-org/example-app"},
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, NewImageLabels(tt.labels).Extra)
		})
	}
}

func TestNormalizeExtraKey(t *testing.T) {
	tests := []struct {
		suffix   string
		expected string
	}{
		{suffix: "team", expected: "team"},
		{suffix: "Team", expected: "team"},
		{suffix: "runbook.url", expected: "runbook_url"},
		{suffix: "cost-center", expected: "cost_center"},
		{suffix: "owner/group", expected: "owner_group"},
		{suffix: "_padded_", expected: "padded"},
		{suffix: "!!!", expected: ""},
		{suffix: "", expected: ""},
	}

	for _, tt := range tests {
		t.Run(tt.suffix, func(t *testing.T) {
			assert.Equal(t, tt.expected, normalizeExtraKey(tt.suffix))
		})
	}
}

func TestImageManifestExtraRoundTrip(t *testing.T) {
	manifest := NewImageManifest("registry/app", ImageLabels{
		Extra: map[string]string{
			"team":        "platform",
			"cost_center": "platform",
			"runbook_url": "https://example.com/runbook",
		},
	})

	var document ImageManifestDocument
	document.Upsert(manifest)

	lines, err := RenderImageManifestComment(document)
	require.NoError(t, err)

	rendered := strings.Join(lines, "\n")
	assert.Contains(t, rendered, "#     extra:\n")
	assert.Contains(t, rendered, "#       team: platform\n")
	assert.Contains(t, rendered, "#       runbook_url: https://example.com/runbook\n")

	// キーの並びは決定的であること
	again, err := RenderImageManifestComment(document)
	require.NoError(t, err)
	assert.Equal(t, lines, again)

	parsed, err := ParseImageManifestComment(lines)
	require.NoError(t, err)
	require.Len(t, parsed.Images, 1)
	assert.Equal(t, manifest.Extra, parsed.Images[0].Extra)
}
