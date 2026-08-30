package kustomize

import (
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strconv"
	"strings"

	yaml "go.yaml.in/yaml/v3"
	"sigs.k8s.io/kustomize/api/types"

	"github.com/murasame29/image-updater/internal/model"
)

const newTagField = "newTag"

// document is a line oriented view of a kustomization.yaml.
//
// Only the lines that have to change are rewritten, so comments, blank lines,
// key order and fields unknown to types.Kustomization survive a round trip.
type document struct {
	lines              []string
	hasTrailingNewline bool

	// imagesKeyLine is the 1-based line of the `images` key.
	imagesKeyLine int
	imagesIndent  int
	imageNodes    []*yaml.Node
	images        []types.Image
}

// parse reads a kustomization.yaml and locates its images block.
//
// Returns:
//
//	The parsed document, or an error when the file is not a mapping, and
//	ErrImageNotManaged when it has no images block.
func parse(data []byte) (*document, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("failed to unmarshal the kustomization file: %w", err)
	}

	if len(root.Content) == 0 {
		return nil, fmt.Errorf("%w: the kustomization file is empty", model.ErrImageNotManaged)
	}

	mapping := root.Content[0]
	if mapping.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("the kustomization file is not a mapping")
	}

	text := string(data)
	doc := &document{hasTrailingNewline: strings.HasSuffix(text, "\n")}
	doc.lines = strings.Split(strings.TrimSuffix(text, "\n"), "\n")

	for i := 0; i+1 < len(mapping.Content); i += 2 {
		key, value := mapping.Content[i], mapping.Content[i+1]
		if key.Value != "images" {
			continue
		}
		if value.Kind != yaml.SequenceNode {
			return nil, fmt.Errorf("the images key is not a sequence")
		}
		if err := value.Decode(&doc.images); err != nil {
			return nil, fmt.Errorf("failed to decode the images block: %w", err)
		}

		doc.imagesKeyLine = key.Line
		doc.imagesIndent = key.Column - 1
		doc.imageNodes = value.Content
		break
	}

	if doc.imagesKeyLine == 0 {
		return nil, fmt.Errorf("%w: the kustomization file has no images block", model.ErrImageNotManaged)
	}

	return doc, nil
}

// Images returns the decoded images block.
func (d *document) Images() []types.Image {
	return d.images
}

// bytes renders the document back to YAML.
func (d *document) bytes() []byte {
	text := strings.Join(d.lines, "\n")
	if d.hasTrailingNewline {
		text += "\n"
	}
	return []byte(text)
}

// setNewTags rewrites `newTag` of the given images block indexes.
// Edits run from the bottom of the file upwards so that the line numbers
// recorded while parsing stay valid.
func (d *document) setNewTags(tags map[int]string) error {
	for _, index := range slices.Backward(slices.Sorted(maps.Keys(tags))) {
		if err := d.setNewTag(index, tags[index]); err != nil {
			return err
		}
	}
	return nil
}

func (d *document) setNewTag(index int, tag string) error {
	if index < 0 || index >= len(d.imageNodes) {
		return fmt.Errorf("images index out of range: %d", index)
	}

	item := d.imageNodes[index]
	if item.Kind != yaml.MappingNode {
		return fmt.Errorf("images[%d] is not a mapping", index)
	}

	for i := 0; i+1 < len(item.Content); i += 2 {
		if item.Content[i].Value == newTagField {
			return d.replaceScalar(item.Content[i+1], tag)
		}
	}

	return d.appendMappingEntry(item, newTagField, tag)
}

// upsertImageManifest merges manifest into the managed metadata comment block
// placed above the images key, creating the block when it does not exist yet.
// User written comments around the block are left untouched.
//
// Args:
//
//	manifest: the entry to insert or replace.
//	indent: spaces per nesting level inside the block.
func (d *document) upsertImageManifest(manifest model.ImageManifest, indent int) error {
	regionStart, regionEnd := d.headCommentRange()
	blockStart, blockEnd, found := findManifestBlock(d.lines, regionStart, regionEnd)

	var manifestDocument model.ImageManifestDocument
	if found {
		parsed, err := model.ParseImageManifestComment(d.lines[blockStart:blockEnd])
		if err != nil {
			slog.Warn("failed to parse the existing image manifest metadata, regenerating.", "error", err)
		} else {
			manifestDocument = parsed
		}
	}

	manifestDocument.Upsert(manifest)

	commentLines, err := model.RenderImageManifestComment(manifestDocument, indent)
	if err != nil {
		return err
	}

	// The block sits at the same column as the images key it documents.
	margin := strings.Repeat(" ", d.imagesIndent)
	rendered := make([]string, 0, len(commentLines))
	for _, line := range commentLines {
		rendered = append(rendered, margin+line)
	}

	if found {
		d.lines = slices.Concat(d.lines[:blockStart], rendered, d.lines[blockEnd:])
		d.shift(len(rendered) - (blockEnd - blockStart))
		return nil
	}

	d.lines = slices.Concat(d.lines[:regionStart], rendered, d.lines[regionStart:])
	d.shift(len(rendered))
	return nil
}

// shift moves the recorded line numbers of the images block by delta, keeping
// them valid after lines were inserted or removed above the images key.
func (d *document) shift(delta int) {
	if delta == 0 {
		return
	}

	d.imagesKeyLine += delta
	for _, item := range d.imageNodes {
		shiftNode(item, delta)
	}
}

func shiftNode(node *yaml.Node, delta int) {
	node.Line += delta
	for _, child := range node.Content {
		shiftNode(child, delta)
	}
}

// headCommentRange returns the [start, end) range of contiguous comment lines
// directly above the images key, as 0-based line indexes.
func (d *document) headCommentRange() (int, int) {
	end := d.imagesKeyLine - 1
	start := end
	for start > 0 && isCommentLine(d.lines[start-1]) {
		start--
	}
	return start, end
}

// findManifestBlock locates the marker delimited block inside lines[start:end).
func findManifestBlock(lines []string, start, end int) (int, int, bool) {
	blockStart := -1
	for i := start; i < end && i < len(lines); i++ {
		switch {
		case model.IsImageManifestBegin(lines[i]):
			blockStart = i
		case model.IsImageManifestEnd(lines[i]) && blockStart >= 0:
			return blockStart, i + 1, true
		}
	}
	return 0, 0, false
}

// replaceScalar swaps the value of a scalar node in place, keeping whatever
// follows it on the line (an inline comment, for instance).
func (d *document) replaceScalar(node *yaml.Node, value string) error {
	if node.Kind != yaml.ScalarNode {
		return fmt.Errorf("%s is not a scalar", newTagField)
	}

	lineIndex := node.Line - 1
	if lineIndex < 0 || lineIndex >= len(d.lines) {
		return fmt.Errorf("%s line out of range: %d", newTagField, node.Line)
	}

	line := d.lines[lineIndex]
	start := node.Column - 1
	if start < 0 || start > len(line) {
		return fmt.Errorf("%s column out of range: %d", newTagField, node.Column)
	}

	length, err := scalarTokenLength(line[start:], node.Style)
	if err != nil {
		return err
	}

	d.lines[lineIndex] = line[:start] + formatScalar(value) + line[start+length:]
	return nil
}

// appendMappingEntry adds `key: value` below the last entry of a mapping node.
func (d *document) appendMappingEntry(item *yaml.Node, key, value string) error {
	lastLine := 0
	for _, node := range item.Content {
		if node.Line > lastLine {
			lastLine = node.Line
		}
	}

	if lastLine <= 0 || lastLine > len(d.lines) {
		return fmt.Errorf("failed to locate the mapping entry to append %s after", key)
	}

	entry := fmt.Sprintf("%s%s: %s", strings.Repeat(" ", item.Column-1), key, formatScalar(value))
	d.lines = slices.Insert(d.lines, lastLine, entry)
	return nil
}

// scalarTokenLength returns the byte length the scalar occupies in the source line.
func scalarTokenLength(rest string, style yaml.Style) (int, error) {
	switch style {
	case yaml.SingleQuotedStyle:
		return quotedTokenLength(rest, '\'')
	case yaml.DoubleQuotedStyle:
		return quotedTokenLength(rest, '"')
	case 0:
		return plainTokenLength(rest), nil
	default:
		return 0, fmt.Errorf("unsupported scalar style: %d", style)
	}
}

func plainTokenLength(rest string) int {
	end := len(rest)
	if index := strings.Index(rest, " #"); index >= 0 {
		end = index
	}
	return len(strings.TrimRight(rest[:end], " \t"))
}

func quotedTokenLength(rest string, quote byte) (int, error) {
	if len(rest) == 0 || rest[0] != quote {
		return 0, fmt.Errorf("quoted scalar does not start with %q", quote)
	}

	for i := 1; i < len(rest); i++ {
		switch rest[i] {
		case '\\':
			if quote == '"' {
				i++
			}
		case quote:
			// '' inside a single quoted scalar is an escaped quote, not the end.
			if quote == '\'' && i+1 < len(rest) && rest[i+1] == '\'' {
				i++
				continue
			}
			return i + 1, nil
		}
	}

	return 0, fmt.Errorf("unterminated quoted scalar")
}

// formatScalar quotes a value that YAML would not read back as the same string.
func formatScalar(value string) string {
	if isSafePlainScalar(value) {
		return value
	}
	return strconv.Quote(value)
}

func isSafePlainScalar(value string) bool {
	if value == "" || value[0] == '.' || value[0] == '-' {
		return false
	}

	for i := 0; i < len(value); i++ {
		char := value[i]
		switch {
		case char >= 'a' && char <= 'z',
			char >= 'A' && char <= 'Z',
			char >= '0' && char <= '9',
			char == '_', char == '.', char == '-':
		default:
			return false
		}
	}

	var decoded any
	if err := yaml.Unmarshal([]byte(value), &decoded); err != nil {
		return false
	}
	_, isString := decoded.(string)
	return isString
}

func isCommentLine(line string) bool {
	return strings.HasPrefix(strings.TrimLeft(line, " \t"), "#")
}
