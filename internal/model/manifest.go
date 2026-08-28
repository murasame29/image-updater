package model

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	yaml "go.yaml.in/yaml/v3"
)

const (
	// ImageManifestSchemaVersion is the schema version of the well-known image manifest.
	ImageManifestSchemaVersion = "v1"

	// ImageManifestGenerator identifies the writer of the manifest block.
	ImageManifestGenerator = "image-updater"

	// ImageManifestBeginMarker and ImageManifestEndMarker delimit the managed
	// comment block inside kustomization.yaml.
	ImageManifestBeginMarker = "--- BEGIN image-updater metadata (auto-generated, do not edit) ---"
	ImageManifestEndMarker   = "--- END image-updater metadata ---"

	commentPrefix = "#"
)

// ImageManifest is the well-known metadata of a single container image.
// Every field is derived from the OCI / build labels baked into the image, so
// the manifest alone tells where the code, the commit and the CI run live.
type ImageManifest struct {
	// Image is the registry URI without a tag. It matches `images[].name`.
	Image string `yaml:"image"`
	// GitHubRepo is the repository that holds the source code (org.opencontainers.image.source).
	GitHubRepo string `yaml:"github_repo,omitempty"`
	// GitSHA is the commit the image was built from (org.opencontainers.image.revision).
	GitSHA string `yaml:"git_sha,omitempty"`
	// CommitURL points at GitSHA inside GitHubRepo.
	CommitURL string `yaml:"commit_url,omitempty"`
	// BuiltAt is the image build time (org.opencontainers.image.created).
	BuiltAt string `yaml:"built_at,omitempty"`
	// BuildRunURL points at the CI run that built the image (org.opencontainers.image.build.url).
	BuildRunURL string `yaml:"build_run_url,omitempty"`
	// Extra carries the custom labels under LabelExtraPrefix as-is.
	Extra map[string]string `yaml:"extra,omitempty"`
}

// ImageManifestDocument is the document rendered into the managed comment block.
type ImageManifestDocument struct {
	SchemaVersion string          `yaml:"schema_version"`
	Generator     string          `yaml:"generator"`
	Images        []ImageManifest `yaml:"images"`
}

// NewImageManifest builds the well-known manifest of a single image.
//
// Args:
//
//	image: registry URI without a tag.
//	labels: labels read from the image config blob.
func NewImageManifest(image string, labels ImageLabels) ImageManifest {
	manifest := ImageManifest{
		Image:       image,
		GitHubRepo:  labels.Source,
		GitSHA:      labels.Revision,
		BuiltAt:     labels.Created,
		BuildRunURL: labels.BuildURL,
		Extra:       labels.Extra,
	}

	if labels.Source != "" && labels.Revision != "" {
		manifest.CommitURL = fmt.Sprintf("%s/commit/%s", labels.Source, labels.Revision)
	}

	return manifest
}

// Upsert replaces the entry of the same image and keeps the remaining entries.
func (d *ImageManifestDocument) Upsert(manifest ImageManifest) {
	d.SchemaVersion = ImageManifestSchemaVersion
	d.Generator = ImageManifestGenerator

	replaced := false
	for i := range d.Images {
		if d.Images[i].Image == manifest.Image {
			d.Images[i] = manifest
			replaced = true
			break
		}
	}

	if !replaced {
		d.Images = append(d.Images, manifest)
	}

	sort.SliceStable(d.Images, func(i, j int) bool { return d.Images[i].Image < d.Images[j].Image })
}

// RenderImageManifestComment renders the document as YAML comment lines,
// markers included. Lines carry the leading "#" but no indentation.
//
// Returns:
//
//	The comment lines, or an error when the document cannot be marshalled.
func RenderImageManifestComment(document ImageManifestDocument) ([]string, error) {
	document.SchemaVersion = ImageManifestSchemaVersion
	document.Generator = ImageManifestGenerator

	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	if err := encoder.Encode(document); err != nil {
		return nil, fmt.Errorf("failed to marshal image manifest: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("failed to marshal image manifest: %w", err)
	}

	body := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")

	lines := make([]string, 0, len(body)+2)
	lines = append(lines, commentLine(ImageManifestBeginMarker))
	for _, line := range body {
		lines = append(lines, commentLine(line))
	}
	lines = append(lines, commentLine(ImageManifestEndMarker))

	return lines, nil
}

// ParseImageManifestComment reads back a rendered comment block.
// Marker lines and indentation are tolerated, so the raw lines taken from a
// kustomization.yaml can be passed as-is.
//
// Returns:
//
//	The parsed document, or an error when the block is not valid YAML.
func ParseImageManifestComment(lines []string) (ImageManifestDocument, error) {
	body := make([]string, 0, len(lines))
	for _, line := range lines {
		content, ok := uncommentLine(line)
		if !ok {
			continue
		}
		if IsImageManifestBegin(line) || IsImageManifestEnd(line) {
			continue
		}
		body = append(body, content)
	}

	var document ImageManifestDocument
	if err := yaml.Unmarshal([]byte(strings.Join(body, "\n")), &document); err != nil {
		return ImageManifestDocument{}, fmt.Errorf("failed to unmarshal image manifest: %w", err)
	}

	return document, nil
}

// IsImageManifestBegin reports whether the line opens a managed manifest block.
func IsImageManifestBegin(line string) bool {
	return isMarker(line, ImageManifestBeginMarker)
}

// IsImageManifestEnd reports whether the line closes a managed manifest block.
func IsImageManifestEnd(line string) bool {
	return isMarker(line, ImageManifestEndMarker)
}

func isMarker(line, marker string) bool {
	content, ok := uncommentLine(line)
	return ok && strings.TrimSpace(content) == marker
}

func commentLine(content string) string {
	if content == "" {
		return commentPrefix
	}
	return commentPrefix + " " + content
}

// uncommentLine strips indentation and the comment prefix of a YAML comment line.
func uncommentLine(line string) (string, bool) {
	trimmed := strings.TrimLeft(line, " \t")
	if !strings.HasPrefix(trimmed, commentPrefix) {
		return "", false
	}
	return strings.TrimPrefix(strings.TrimPrefix(trimmed, commentPrefix), " "), true
}
