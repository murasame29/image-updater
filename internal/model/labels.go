package model

import "strings"

const (
	// OCI standard labels (auto from docker/metadata-action)
	LabelSource   = "org.opencontainers.image.source"
	LabelRevision = "org.opencontainers.image.revision"
	LabelCreated  = "org.opencontainers.image.created"

	// Labels outside the OCI spec, attached by the pipeline that builds the
	// image. They are all optional: an image without them still gets updated,
	// only the pull request carries less context.
	LabelPRNumber   = "org.opencontainers.image.pr.number"
	LabelPRAuthor   = "org.opencontainers.image.pr.author"
	LabelPRTitle    = "org.opencontainers.image.pr.title"
	LabelBuildURL   = "org.opencontainers.image.build.url"
	LabelBuildRunID = "org.opencontainers.image.build.run_id"
	LabelBuildRef   = "org.opencontainers.image.build.ref"
	LabelBuildEvent = "org.opencontainers.image.build.event"
	LabelBuildActor = "org.opencontainers.image.build.actor"

	// LabelExtraPrefix namespaces the custom labels surfaced as-is in the
	// well-known image manifest. The suffix becomes the manifest key.
	// e.g. org.opencontainers.image.extra.team -> extra.team
	LabelExtraPrefix = "org.opencontainers.image.extra."
)

type ImageLabels struct {
	// OCI standard
	Source   string
	Revision string
	Created  string

	// Custom
	PRNumber   string
	PRAuthor   string
	PRTitle    string
	BuildURL   string
	BuildRunID string
	BuildRef   string
	BuildEvent string
	BuildActor string

	// Extra holds the labels under LabelExtraPrefix, keyed by the normalized suffix.
	Extra map[string]string

	// ECR image info (set by caller, not from labels)
	ImageURI       string
	ImageSizeBytes int64
}

// NewImageLabels reads the well-known labels out of the raw image config labels.
//
// Every value is normalised on the way in, because the labels were chosen by
// whoever pushed the image. See label_value.go for what that means per field. A
// value that cannot be normalised is dropped, so a consumer only ever sees
// something it can safely render.
func NewImageLabels(labels map[string]string) ImageLabels {
	return ImageLabels{
		Source:     sanitizeLabelURL(labels[LabelSource]),
		Revision:   sanitizeLabelToken(labels[LabelRevision]),
		Created:    sanitizeLabelToken(labels[LabelCreated]),
		PRNumber:   sanitizeLabelNumber(labels[LabelPRNumber]),
		PRAuthor:   sanitizeLabelHandle(labels[LabelPRAuthor]),
		PRTitle:    sanitizeLabelText(labels[LabelPRTitle]),
		BuildURL:   sanitizeLabelURL(labels[LabelBuildURL]),
		BuildRunID: sanitizeLabelToken(labels[LabelBuildRunID]),
		BuildRef:   sanitizeLabelToken(labels[LabelBuildRef]),
		BuildEvent: sanitizeLabelToken(labels[LabelBuildEvent]),
		BuildActor: sanitizeLabelHandle(labels[LabelBuildActor]),
		Extra:      newExtraLabels(labels),
	}
}

// newExtraLabels collects the custom labels under LabelExtraPrefix.
// Empty values and keys that normalize to nothing are dropped, and the values are
// normalised the same way as a free-form label.
//
// Returns:
//
//	The custom labels keyed by the normalized suffix, or nil when there is none.
func newExtraLabels(labels map[string]string) map[string]string {
	extra := make(map[string]string)

	for key, value := range labels {
		suffix, ok := strings.CutPrefix(key, LabelExtraPrefix)
		if !ok {
			continue
		}

		normalized := normalizeExtraKey(suffix)
		if normalized == "" {
			continue
		}

		sanitized := sanitizeLabelText(value)
		if sanitized == "" {
			continue
		}

		extra[normalized] = sanitized
	}

	if len(extra) == 0 {
		return nil
	}

	return extra
}

// normalizeExtraKey turns a label suffix into a snake_case manifest key.
// Separators become underscores and any other character is dropped.
func normalizeExtraKey(suffix string) string {
	var builder strings.Builder

	for _, char := range strings.ToLower(suffix) {
		switch {
		case char >= 'a' && char <= 'z', char >= '0' && char <= '9', char == '_':
			builder.WriteRune(char)
		case char == '.', char == '-', char == '/':
			builder.WriteRune('_')
		}
	}

	return strings.Trim(builder.String(), "_")
}
