package updater

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/murasame29/image-updater/internal/model"
)

// FormatPullRequestBody renders the description of an image update pull request
// from the labels baked into the image.
//
// Which facts are worth surfacing is a use case decision, so it lives here
// rather than in a git host adapter. The output is Markdown, which GitHub and
// GitLab both read.
func FormatPullRequestBody(labels model.ImageLabels) string {
	var b strings.Builder

	if labels.PRTitle != "" {
		fmt.Fprintf(&b, "## 📦 Image Update: %s\n\n", escapeMarkdown(labels.PRTitle))
	} else {
		b.WriteString("## 📦 Image Update\n\n")
	}

	writeChanges(&b, labels)
	writeLinks(&b, labels)
	writeLabels(&b, labels)

	return b.String()
}

func writeChanges(b *strings.Builder, labels model.ImageLabels) {
	if !hasChangesContent(labels) {
		return
	}

	b.WriteString("### Changes\n\n")
	b.WriteString("| | |\n")
	b.WriteString("|---|---|\n")

	if labels.Source != "" && labels.Revision != "" {
		commitURL := fmt.Sprintf("%s/commit/%s", labels.Source, labels.Revision)
		fmt.Fprintf(b, "| **Commit** | [`%s`](%s) |\n", shortRevision(labels.Revision), commitURL)
	}

	if labels.BuildRef != "" {
		fmt.Fprintf(b, "| **Branch** | `%s` |\n", labels.BuildRef)
	}

	if labels.PRAuthor != "" {
		fmt.Fprintf(b, "| **Author** | @%s |\n", labels.PRAuthor)
	}

	if labels.BuildEvent != "" {
		fmt.Fprintf(b, "| **Trigger** | `%s` |\n", labels.BuildEvent)
	}

	if labels.ImageSizeBytes > 0 {
		fmt.Fprintf(b, "| **Image Size** | %s |\n", formatBytes(labels.ImageSizeBytes))
	}

	b.WriteString("\n")
}

func writeLinks(b *strings.Builder, labels model.ImageLabels) {
	if !hasLinksContent(labels) {
		return
	}

	b.WriteString("### Links\n\n")

	var links []string

	if labels.PRNumber != "" && labels.Source != "" {
		prURL := fmt.Sprintf("%s/pull/%s", labels.Source, labels.PRNumber)
		links = append(links, fmt.Sprintf("🔗 [Source PR #%s](%s)", labels.PRNumber, prURL))
	}

	if labels.BuildURL != "" && labels.BuildRunID != "" {
		links = append(links, fmt.Sprintf("⚙️ [CI Run](%s)", labels.BuildURL))
	}

	b.WriteString(strings.Join(links, " · "))
	b.WriteString("\n\n")
}

func writeLabels(b *strings.Builder, labels model.ImageLabels) {
	if labels.ImageURI == "" && labels.Revision == "" && labels.Created == "" {
		return
	}

	b.WriteString("<details>\n")
	b.WriteString("<summary>📁 Image Labels</summary>\n\n")
	b.WriteString("```\n")

	if labels.ImageURI != "" {
		fmt.Fprintf(b, "image: %s\n", labels.ImageURI)
	}

	if labels.Created != "" {
		fmt.Fprintf(b, "created: %s\n", labels.Created)
	}

	if labels.Revision != "" {
		fmt.Fprintf(b, "revision: %s\n", labels.Revision)
	}

	b.WriteString("```\n\n")
	b.WriteString("</details>\n")
}

func hasChangesContent(labels model.ImageLabels) bool {
	return (labels.Source != "" && labels.Revision != "") ||
		labels.BuildRef != "" ||
		labels.PRAuthor != "" ||
		labels.BuildEvent != "" ||
		labels.ImageSizeBytes > 0
}

func hasLinksContent(labels model.ImageLabels) bool {
	return (labels.PRNumber != "" && labels.Source != "") ||
		(labels.BuildURL != "" && labels.BuildRunID != "")
}

// shortRevision abbreviates a commit hash the way git does.
func shortRevision(revision string) string {
	const shortLength = 7
	if len(revision) > shortLength {
		return revision[:shortLength]
	}
	return revision
}

// markdownMetaCharacters are the characters a label value could otherwise use to
// take over the description: code spans, emphasis, links, raw HTML, table cells,
// issue references and @mentions.
//
// Constructs that only work at the start of a line, such as a heading or a list
// item, are not in here. Label values have their line breaks removed on the way
// into the domain, so a value can never reach the start of a line, and escaping
// `-` or `.` as well would only make honest titles look mangled.
const markdownMetaCharacters = "\\`*_[]<>|@#&~"

// escapeMarkdown renders a value as literal text.
//
// The label it comes from was chosen by whoever pushed the image, which is a much
// lower bar than being allowed to approve the pull request this description ends
// up on. A backslash escape works for every ASCII punctuation character in
// CommonMark, which is what GitHub renders.
func escapeMarkdown(value string) string {
	var b strings.Builder
	b.Grow(len(value) * 2)

	for _, char := range value {
		if char < utf8.RuneSelf && strings.ContainsRune(markdownMetaCharacters, char) {
			b.WriteByte('\\')
		}
		b.WriteRune(char)
	}

	return b.String()
}

func formatBytes(bytes int64) string {
	const (
		kb = 1024
		mb = kb * 1024
		gb = mb * 1024
	)

	switch {
	case bytes >= gb:
		return fmt.Sprintf("%.2f GB", float64(bytes)/float64(gb))
	case bytes >= mb:
		return fmt.Sprintf("%.2f MB", float64(bytes)/float64(mb))
	case bytes >= kb:
		return fmt.Sprintf("%.2f KB", float64(bytes)/float64(kb))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}
