package updater

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/murasame29/image-updater/internal/model"
)

func TestFormatPullRequestBody(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		labels      model.ImageLabels
		contains    []string
		notContains []string
	}{
		{
			name: "全てのラベルが揃っている場合",
			labels: model.ImageLabels{
				Source:         "https://github.com/example-org/example-ci",
				Revision:       "abc1234def5678",
				Created:        "2026-01-20T02:18:51.355Z",
				PRNumber:       "123",
				PRAuthor:       "username",
				PRTitle:        "Add new feature",
				BuildURL:       "https://github.com/example-org/example-ci/actions/runs/12345",
				BuildRunID:     "12345",
				BuildRef:       "main",
				BuildEvent:     "push",
				ImageURI:       "123456789012.dkr.ecr.ap-northeast-1.amazonaws.com/apps/app:abc1234",
				ImageSizeBytes: 5 * 1024 * 1024,
			},
			contains: []string{
				"## 📦 Image Update: Add new feature",
				"### Changes",
				"| **Commit** | [`abc1234`](https://github.com/example-org/example-ci/commit/abc1234def5678) |",
				"| **Branch** | `main` |",
				"| **Author** | @username |",
				"| **Trigger** | `push` |",
				"| **Image Size** | 5.00 MB |",
				"### Links",
				"🔗 [Source PR #123](https://github.com/example-org/example-ci/pull/123)",
				"⚙️ [CI Run](https://github.com/example-org/example-ci/actions/runs/12345)",
				"<details>",
				"<summary>📁 Image Labels</summary>",
				"image: 123456789012.dkr.ecr.ap-northeast-1.amazonaws.com/apps/app:abc1234",
				"created: 2026-01-20T02:18:51.355Z",
				"revision: abc1234def5678",
				"</details>",
			},
		},
		{
			name:     "ラベルが空ならヘッダーだけ",
			labels:   model.ImageLabels{},
			contains: []string{"## 📦 Image Update\n"},
			notContains: []string{
				"### Changes",
				"### Links",
				"<details>",
			},
		},
		{
			name: "PR タイトルがなければヘッダーにコロンを付けない",
			labels: model.ImageLabels{
				Source:   "https://github.com/example-org/example-ci",
				Revision: "abc1234",
			},
			contains:    []string{"## 📦 Image Update\n"},
			notContains: []string{"## 📦 Image Update:"},
		},
		{
			name:     "PR タイトルがあればヘッダーに出す",
			labels:   model.ImageLabels{PRTitle: "Fix bug"},
			contains: []string{"## 📦 Image Update: Fix bug"},
		},
		{
			name: "source がなければ commit 行を出さない",
			labels: model.ImageLabels{
				Revision:   "abc1234def5678",
				BuildRef:   "main",
				BuildEvent: "push",
			},
			contains: []string{
				"### Changes",
				"| **Branch** | `main` |",
				"| **Trigger** | `push` |",
			},
			notContains: []string{"**Commit**"},
		},
		{
			name: "run ID がなければ CI Run リンクを出さない",
			labels: model.ImageLabels{
				Source:   "https://github.com/example-org/example-ci",
				PRNumber: "123",
				BuildURL: "https://github.com/example-org/example-ci/actions/runs/12345",
			},
			contains:    []string{"### Links", "🔗 [Source PR #123]"},
			notContains: []string{"⚙️ [CI Run]"},
		},
		{
			name:        "リンクが1つもなければ Links セクションを出さない",
			labels:      model.ImageLabels{BuildRef: "main"},
			contains:    []string{"### Changes", "| **Branch** | `main` |"},
			notContains: []string{"### Links"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			body := FormatPullRequestBody(tt.labels)
			for _, want := range tt.contains {
				assert.Contains(t, body, want)
			}
			for _, unwanted := range tt.notContains {
				assert.NotContains(t, body, unwanted)
			}
		})
	}
}

func TestFormatBytes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		bytes int64
		want  string
	}{
		{name: "バイト", bytes: 512, want: "512 B"},
		{name: "キロバイト", bytes: 2048, want: "2.00 KB"},
		{name: "メガバイト", bytes: 3 * 1024 * 1024, want: "3.00 MB"},
		{name: "ギガバイト", bytes: 2 * 1024 * 1024 * 1024, want: "2.00 GB"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, formatBytes(tt.bytes))
		})
	}
}

func TestShortRevision(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "abc1234", shortRevision("abc1234def5678"))
	assert.Equal(t, "abc12", shortRevision("abc12"))
}
