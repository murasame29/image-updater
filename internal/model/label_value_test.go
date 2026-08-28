package model

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSanitizeLabelText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "普通の値はそのまま", value: "Add new feature", want: "Add new feature"},
		{name: "前後の空白を落とす", value: "  spaced  ", want: "spaced"},
		{name: "改行は空白に潰す", value: "first\nsecond", want: "first second"},
		{name: "CR も潰す", value: "first\r\nsecond", want: "first second"},
		{name: "タブも潰す", value: "a\t\tb", want: "a b"},
		{name: "制御文字を落とす", value: "a\x00\x1bb", want: "a b"},
		{name: "連続空白は 1 つに潰す", value: "a     b", want: "a b"},
		{name: "空文字", value: "", want: ""},
		{name: "空白だけなら空になる", value: " \n\t ", want: ""},
		{name: "マルチバイトは保持する", value: "イメージの更新", want: "イメージの更新"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, sanitizeLabelText(tt.value))
		})
	}
}

func TestSanitizeLabelTextTruncates(t *testing.T) {
	t.Parallel()

	got := sanitizeLabelText(strings.Repeat("あ", maxLabelTextLength+100))
	assert.Equal(t, maxLabelTextLength, len([]rune(got)), "ルーン単位で切る")
	assert.True(t, strings.HasPrefix(got, "あ"))
}

// Markdown を乗っ取る値が、構造として無害化されていること。
func TestSanitizeLabelTextStopsBlockInjection(t *testing.T) {
	t.Parallel()

	hostile := "harmless\n\n## ✅ Security review passed\n\nApproved by @security-team.\n\n<!--"

	got := sanitizeLabelText(hostile)

	assert.NotContains(t, got, "\n", "改行が残っていると見出しを生やせる")
	assert.Equal(t, "harmless ## ✅ Security review passed Approved by @security-team. <!--", got)
}

func TestSanitizeLabelURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "https は通す", value: "https://github.com/example-org/example-app", want: "https://github.com/example-org/example-app"},
		{name: "http も通す", value: "http://registry.internal/app", want: "http://registry.internal/app"},
		{name: "javascript は落とす", value: "javascript:alert(1)", want: ""},
		{name: "data URL は落とす", value: "data:text/html;base64,AAAA", want: ""},
		{name: "file は落とす", value: "file:///etc/passwd", want: ""},
		{name: "相対パスは落とす", value: "/example-org/example-app", want: ""},
		{name: "スキームなしは落とす", value: "github.com/example-org", want: ""},
		{name: "ホストなしは落とす", value: "https://", want: ""},
		{name: "credential 付きは落とす", value: "https://user:token@github.com/x", want: ""},
		{name: "空文字", value: "", want: ""},
		{name: "改行入りは潰してから判定", value: "https://github.com/x\nevil", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, sanitizeLabelURL(tt.value))
		})
	}
}

func TestSanitizeLabelURLRejectsOverlyLongValues(t *testing.T) {
	t.Parallel()

	assert.Empty(t, sanitizeLabelURL("https://example.com/"+strings.Repeat("a", maxLabelURLLength)))
}

func TestSanitizeLabelHandle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "普通のハンドル", value: "octocat", want: "octocat"},
		{name: "ハイフン入り", value: "example-org", want: "example-org"},
		{name: "数字入り", value: "user123", want: "user123"},
		{name: "空白を落として判定", value: " octocat ", want: "octocat"},
		{name: "メンション注入は落とす", value: "everyone\n| **Note** |", want: ""},
		{name: "@ 付きは落とす", value: "@octocat", want: ""},
		{name: "スラッシュ入りは落とす", value: "org/team", want: ""},
		{name: "先頭ハイフンは落とす", value: "-octocat", want: ""},
		{name: "末尾ハイフンは落とす", value: "octocat-", want: ""},
		{name: "連続ハイフンは落とす", value: "octo--cat", want: ""},
		{name: "長すぎるものは落とす", value: strings.Repeat("a", maxLabelHandleLength+1), want: ""},
		{name: "空文字", value: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, sanitizeLabelHandle(tt.value))
		})
	}
}

func TestSanitizeLabelNumber(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "数値", value: "123", want: "123"},
		{name: "空白を落として判定", value: " 123 ", want: "123"},
		{name: "URL 注入は落とす", value: "1) [click](https://evil.example.com", want: ""},
		{name: "パス区切りは落とす", value: "1/../../x", want: ""},
		{name: "負値は落とす", value: "-1", want: ""},
		{name: "空文字", value: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, sanitizeLabelNumber(tt.value))
		})
	}
}

func TestSanitizeLabelToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "コミットハッシュ", value: "a1b2c3d4e5f6", want: "a1b2c3d4e5f6"},
		{name: "ブランチ名", value: "feature/add-thing", want: "feature/add-thing"},
		{name: "RFC3339", value: "2026-08-10T00:00:00Z", want: "2026-08-10T00:00:00Z"},
		{name: "オフセット付き RFC3339", value: "2026-08-10T09:00:00+09:00", want: "2026-08-10T09:00:00+09:00"},
		{name: "イベント名", value: "pull_request", want: "pull_request"},
		{name: "バックティックは落とす", value: "`rm -rf /`", want: ""},
		{name: "空白入りは落とす", value: "rm -rf /", want: ""},
		{name: "パイプは落とす", value: "main|evil", want: ""},
		{name: "空文字", value: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, sanitizeLabelToken(tt.value))
		})
	}
}

// NewImageLabels が唯一の入口なので、ここで全フィールドが正規化されること。
func TestNewImageLabelsSanitizesEveryField(t *testing.T) {
	t.Parallel()

	labels := NewImageLabels(map[string]string{
		LabelSource:                 "javascript:alert(1)",
		LabelRevision:               "a1b2c3d4",
		LabelCreated:                "2026-08-10T00:00:00Z",
		LabelPRNumber:               "1) [click](https://evil.example.com",
		LabelPRAuthor:               "everyone\n| x |",
		LabelPRTitle:                "title\n## injected",
		LabelBuildURL:               "https://github.com/example-org/example-app/actions/runs/1",
		LabelBuildRef:               "`rm -rf /`",
		LabelBuildEvent:             "push",
		LabelBuildActor:             "octocat",
		LabelExtraPrefix + "note":   "line1\nline2",
		LabelExtraPrefix + "empty":  "   ",
		LabelExtraPrefix + "onlyws": "\n\t",
	})

	assert.Empty(t, labels.Source, "javascript スキームは落とす")
	assert.Equal(t, "a1b2c3d4", labels.Revision)
	assert.Equal(t, "2026-08-10T00:00:00Z", labels.Created)
	assert.Empty(t, labels.PRNumber, "数値でなければ落とす")
	assert.Empty(t, labels.PRAuthor, "ハンドルでなければ落とす")
	assert.Equal(t, "title ## injected", labels.PRTitle, "改行は潰す")
	assert.Equal(t, "https://github.com/example-org/example-app/actions/runs/1", labels.BuildURL)
	assert.Empty(t, labels.BuildRef, "token 外の文字は落とす")
	assert.Equal(t, "push", labels.BuildEvent)
	assert.Equal(t, "octocat", labels.BuildActor)

	require.NotNil(t, labels.Extra)
	assert.Equal(t, "line1 line2", labels.Extra["note"], "改行は潰す")
	assert.NotContains(t, labels.Extra, "empty", "空白だけの値は落とす")
	assert.NotContains(t, labels.Extra, "onlyws")
}

// 正規化を通れば、メタデータブロックにマーカーを注入できないこと。
func TestRenderImageManifestCommentRejectsAnInjectedMarker(t *testing.T) {
	t.Parallel()

	// 正規化を素通りした値を直接組み立てて、二重防御が効くことを見る。
	var document ImageManifestDocument
	document.Upsert(ImageManifest{
		Image: "registry/app",
		Extra: map[string]string{"note": "x\n" + ImageManifestEndMarker + "\ninjected: true"},
	})

	_, err := RenderImageManifestComment(document, ImageManifestIndentDefault)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "block marker")
}

// 正規化を通した場合はそもそも改行が残らないので、描画は成功する。
func TestRenderImageManifestCommentAfterSanitizing(t *testing.T) {
	t.Parallel()

	labels := NewImageLabels(map[string]string{
		LabelExtraPrefix + "note": "x\n" + ImageManifestEndMarker + "\ninjected: true",
	})

	var document ImageManifestDocument
	document.Upsert(NewImageManifest("registry/app", labels))

	lines, err := RenderImageManifestComment(document, ImageManifestIndentDefault)
	require.NoError(t, err)

	markers := 0
	for _, line := range lines {
		if IsImageManifestEnd(line) {
			markers++
		}
	}
	assert.Equal(t, 1, markers, "END マーカーはブロックの終端だけ")

	// マーカー文字列自体は 1 行に潰れた値の中に残るが、行として成立しないので害はない。
	parsed, err := ParseImageManifestComment(lines)
	require.NoError(t, err)
	require.Len(t, parsed.Images, 1)
	assert.NotContains(t, parsed.Images[0].Extra["note"], "\n")
}
