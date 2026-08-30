package kustomize

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/murasame29/image-updater/internal/model"
)

const kustomizationFixture = `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization

namespace: sample

# 手書きのコメントは残す
resources:
  - ../../base

images:
  - name: 123456789012.dkr.ecr.ap-northeast-1.amazonaws.com/apps/samples/app
    newTag: alice.790bf3ee04b441a96fb3d1860aea91fa09b72747 # inline comment
  - name: 123456789012.dkr.ecr.ap-northeast-1.amazonaws.com/apps/samples/app
    newTag: bob.054da21f89acc2f24e77f2f910b307c3ad85526d

patches:
  - path: patch.yaml
`

func testManifest(image, revision string) model.ImageManifest {
	return model.NewImageManifest(image, model.ImageLabels{
		Source:   "https://github.com/example-org/example-app",
		Revision: revision,
		Created:  "2026-08-10T00:00:00Z",
		BuildURL: "https://github.com/example-org/example-app/actions/runs/1",
	})
}

func TestParse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		source     string
		shouldFail bool
		imageCount int
	}{
		{
			name:       "images ブロックを持つ kustomization",
			source:     kustomizationFixture,
			imageCount: 2,
		},
		{
			name:       "images ブロックがない場合はエラー",
			source:     "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\n",
			shouldFail: true,
		},
		{
			name:       "マッピングでない場合はエラー",
			source:     "- a\n- b\n",
			shouldFail: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			doc, err := parse([]byte(tt.source))
			if tt.shouldFail {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Len(t, doc.Images(), tt.imageCount)
		})
	}
}

func TestParseReportsAMissingImagesBlockAsUnmanaged(t *testing.T) {
	t.Parallel()

	_, err := parse([]byte("kind: Kustomization\n"))
	require.ErrorIs(t, err, model.ErrImageNotManaged)
}

func TestDocumentSetNewTags(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		source      string
		tags        map[int]string
		contains    []string
		notContains []string
	}{
		{
			name:   "インラインコメントと他のエントリを保持したままタグを更新",
			source: kustomizationFixture,
			tags:   map[int]string{0: "alice.2222222222222222222222222222222222222222"},
			contains: []string{
				"    newTag: alice.2222222222222222222222222222222222222222 # inline comment\n",
				"    newTag: bob.054da21f89acc2f24e77f2f910b307c3ad85526d\n",
				"# 手書きのコメントは残す\n",
				"namespace: sample\n",
				"patches:\n",
			},
			notContains: []string{"790bf3ee04b441a96fb3d1860aea91fa09b72747"},
		},
		{
			name:   "複数エントリを同時に更新",
			source: kustomizationFixture,
			tags: map[int]string{
				0: "alice.aaaa",
				1: "bob.bbbb",
			},
			contains: []string{
				"    newTag: alice.aaaa # inline comment\n",
				"    newTag: bob.bbbb\n",
			},
		},
		{
			name:   "newTag がない場合は追加する",
			source: "kind: Kustomization\nimages:\n  - name: nginx\n",
			tags:   map[int]string{0: "1234567"},
			contains: []string{
				"  - name: nginx\n",
				"    newTag: \"1234567\"\n",
			},
		},
		{
			name:   "数値に読めるタグはクォートする",
			source: kustomizationFixture,
			tags:   map[int]string{1: "1.20"},
			contains: []string{
				"    newTag: \"1.20\"\n",
			},
		},
		{
			name:   "クォート済みの値もそのまま置き換える",
			source: "kind: Kustomization\nimages:\n  - name: nginx\n    newTag: \"1.20\"\n",
			tags:   map[int]string{0: "1.21"},
			contains: []string{
				"    newTag: \"1.21\"\n",
			},
			notContains: []string{"1.20"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			doc, err := parse([]byte(tt.source))
			require.NoError(t, err)
			require.NoError(t, doc.setNewTags(tt.tags))

			got := string(doc.bytes())
			for _, want := range tt.contains {
				assert.Contains(t, got, want)
			}
			for _, unwanted := range tt.notContains {
				assert.NotContains(t, got, unwanted)
			}

			reloaded, err := parse(doc.bytes())
			require.NoError(t, err)
			for index, tag := range tt.tags {
				assert.Equal(t, tag, reloaded.Images()[index].NewTag)
			}
		})
	}
}

func TestDocumentUpsertImageManifest(t *testing.T) {
	t.Parallel()

	const image = "123456789012.dkr.ecr.ap-northeast-1.amazonaws.com/apps/samples/app"

	doc, err := parse([]byte(kustomizationFixture))
	require.NoError(t, err)
	require.NoError(t, doc.upsertImageManifest(testManifest(image, "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678"), model.ImageManifestIndentDefault))

	got := string(doc.bytes())
	for _, want := range []string{
		"# " + model.ImageManifestBeginMarker + "\n",
		"# " + model.ImageManifestEndMarker + "\n",
		"# schema_version: v1\n",
		"#   - image: " + image + "\n",
		"#     github_repo: https://github.com/example-org/example-app\n",
		"#     git_sha: a1b2c3d4e5f60718293a4b5c6d7e8f9012345678\n",
		"#     commit_url: https://github.com/example-org/example-app/commit/a1b2c3d4e5f60718293a4b5c6d7e8f9012345678\n",
		"#     built_at: \"2026-08-10T00:00:00Z\"\n",
		"#     build_run_url: https://github.com/example-org/example-app/actions/runs/1\n",
		"# 手書きのコメントは残す\n",
	} {
		assert.Contains(t, got, want)
	}

	assert.True(t, strings.Contains(got, "# "+model.ImageManifestEndMarker+"\nimages:\n"), "block must sit directly above the images key:\n%s", got)

	_, err = parse(doc.bytes())
	require.NoError(t, err)
}

func TestDocumentUpsertImageManifestMergesEntries(t *testing.T) {
	t.Parallel()

	const image = "123456789012.dkr.ecr.ap-northeast-1.amazonaws.com/apps/samples/app"
	const sidecar = "123456789012.dkr.ecr.ap-northeast-1.amazonaws.com/apps/samples/sidecar"

	doc, err := parse([]byte(kustomizationFixture))
	require.NoError(t, err)
	require.NoError(t, doc.upsertImageManifest(testManifest(image, "1111"), model.ImageManifestIndentDefault))
	require.NoError(t, doc.upsertImageManifest(testManifest(sidecar, "2222"), model.ImageManifestIndentDefault))

	doc, err = parse(doc.bytes())
	require.NoError(t, err)
	require.NoError(t, doc.upsertImageManifest(testManifest(image, "3333"), model.ImageManifestIndentDefault))

	got := string(doc.bytes())
	assert.Equal(t, 1, strings.Count(got, model.ImageManifestBeginMarker), "only one managed block is allowed:\n%s", got)

	start, end := doc.headCommentRange()
	blockStart, blockEnd, found := findManifestBlock(doc.lines, start, end)
	require.True(t, found)

	manifestDocument, err := model.ParseImageManifestComment(doc.lines[blockStart:blockEnd])
	require.NoError(t, err)
	require.Len(t, manifestDocument.Images, 2)
	assert.Equal(t, image, manifestDocument.Images[0].Image)
	assert.Equal(t, "3333", manifestDocument.Images[0].GitSHA)
	assert.Equal(t, sidecar, manifestDocument.Images[1].Image)
	assert.Equal(t, "2222", manifestDocument.Images[1].GitSHA)
}
