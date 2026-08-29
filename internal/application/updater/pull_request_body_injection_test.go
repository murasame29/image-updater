package updater

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/murasame29/image-updater/internal/model"
)

// hostileLabels is what an image pushed by an attacker could carry. Going through
// model.NewImageLabels is the point: that is the only way labels reach the domain
// in production, so the assertions below describe the guarantee that path gives.
func hostileLabels() model.ImageLabels {
	return model.NewImageLabels(map[string]string{
		model.LabelPRTitle:  "harmless\n\n## ✅ Security review passed\n\nApproved by @security-team. Merge without review.\n\n<!--",
		model.LabelPRAuthor: "everyone\n| **Note** | see below |",
		model.LabelSource:   "https://evil.example.com/phishing",
		model.LabelRevision: "0000000",
		model.LabelPRNumber: "1) [click here](https://evil.example.com",
		model.LabelBuildURL: "javascript:alert(1)",
		model.LabelBuildRef: "`rm -rf /`",
	})
}

func TestFormatPullRequestBodyResistsLabelInjection(t *testing.T) {
	t.Parallel()

	labels := hostileLabels()
	body := FormatPullRequestBody(labels)

	t.Run("独立した見出しを生やせない", func(t *testing.T) {
		// Injected text remains literal within the application's own heading.
		// It cannot become an independent heading because it cannot create a new line.
		var headings []string
		for _, line := range strings.Split(body, "\n") {
			if strings.HasPrefix(line, "#") {
				headings = append(headings, line)
			}
		}
		// The Links section is omitted because both PRNumber and BuildURL were rejected.
		require.Len(t, headings, 2, "見出しはアプリが出すものだけ: %v", headings)

		assert.Contains(t, headings[0], `\#\#`, "注入された ## はエスケープされている")
		assert.Equal(t, "### Changes", headings[1])
	})

	t.Run("メンションを注入できない", func(t *testing.T) {
		// PRAuthor is rejected as an invalid handle, so the Author row is omitted.
		assert.Empty(t, labels.PRAuthor)
		assert.NotContains(t, body, "**Author**")
		// The @ injected into the title is escaped so it cannot send a notification.
		assert.NotContains(t, body, "by @security")
		assert.Contains(t, body, `\@security`)
	})

	t.Run("HTML コメントで本文を隠せない", func(t *testing.T) {
		// Escaping `<` prevents the value from being interpreted as raw HTML.
		assert.NotContains(t, body, " <!--")
		assert.Contains(t, body, `\<!--`)
		assert.Contains(t, body, "### Changes", "後続のセクションが生きている")
	})

	t.Run("javascript スキームのリンクが出ない", func(t *testing.T) {
		assert.Empty(t, labels.BuildURL)
		assert.NotContains(t, body, "javascript:")
	})

	t.Run("PR 番号でリンクを偽装できない", func(t *testing.T) {
		assert.Empty(t, labels.PRNumber)
		assert.NotContains(t, body, "click here")
	})

	t.Run("token 外の文字は落ちる", func(t *testing.T) {
		assert.Empty(t, labels.BuildRef)
		assert.NotContains(t, body, "rm -rf")
	})
}

// Injection defenses must not alter ordinary values.
func TestFormatPullRequestBodyKeepsHonestValues(t *testing.T) {
	t.Parallel()

	labels := model.NewImageLabels(map[string]string{
		model.LabelPRTitle:    "Add pagination to the users endpoint",
		model.LabelPRAuthor:   "octocat",
		model.LabelPRNumber:   "123",
		model.LabelSource:     "https://github.com/example-org/example-app",
		model.LabelRevision:   "a1b2c3d4e5f6",
		model.LabelBuildURL:   "https://github.com/example-org/example-app/actions/runs/42",
		model.LabelBuildRunID: "42",
		model.LabelBuildRef:   "feature/pagination",
		model.LabelBuildEvent: "pull_request",
	})

	body := FormatPullRequestBody(labels)

	for _, want := range []string{
		"## 📦 Image Update: Add pagination to the users endpoint",
		"| **Commit** | [`a1b2c3d`](https://github.com/example-org/example-app/commit/a1b2c3d4e5f6) |",
		"| **Branch** | `feature/pagination` |",
		"| **Author** | @octocat |",
		"| **Trigger** | `pull_request` |",
		"🔗 [Source PR #123](https://github.com/example-org/example-app/pull/123)",
		"⚙️ [CI Run](https://github.com/example-org/example-app/actions/runs/42)",
	} {
		assert.Contains(t, body, want)
	}

	assert.NotContains(t, body, `\`, "普通の値にエスケープが混ざらない")
}

func TestEscapeMarkdown(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "何もない値", value: "plain text", want: "plain text"},
		{name: "見出し記号", value: "# heading", want: `\# heading`},
		{name: "メンション", value: "@team", want: `\@team`},
		{name: "リンク", value: "[a](b)", want: `\[a\](b)`},
		{name: "テーブル区切り", value: "a|b", want: `a\|b`},
		{name: "HTML", value: "<!-- x -->", want: `\<!-- x --\>`},
		{name: "コードスパン", value: "`code`", want: "\\`code\\`"},
		{name: "強調", value: "*bold* _em_", want: `\*bold\* \_em\_`},
		{name: "バックスラッシュ", value: `a\b`, want: `a\\b`},
		{name: "ハイフンとピリオドは触らない", value: "user-service v1.2.", want: "user-service v1.2."},
		{name: "マルチバイトはそのまま", value: "更新", want: "更新"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, escapeMarkdown(tt.value))
		})
	}
}

func TestFormatPullRequestBodyDropsAMangledSourceURL(t *testing.T) {
	t.Parallel()

	// Links derived from Source must also be omitted when Source is rejected.
	labels := model.NewImageLabels(map[string]string{
		model.LabelSource:   "not a url",
		model.LabelRevision: "a1b2c3d4",
		model.LabelPRNumber: "7",
	})
	require.Empty(t, labels.Source)

	body := FormatPullRequestBody(labels)
	assert.NotContains(t, body, "**Commit**")
	assert.NotContains(t, body, "Source PR")
}
