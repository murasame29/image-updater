package model

import (
	"log/slog"
	"time"
)

// ImagePushEvent is a registry agnostic "an image was pushed" notification.
//
// Registry adapters translate their provider payload into this shape (an
// EventBridge event for ECR, a Pub/Sub message for Artifact Registry, a webhook
// for Harbor or GHCR), so nothing above the infrastructure layer knows which
// registry is in play.
type ImagePushEvent struct {
	// Kind is the registry the image was pushed to.
	Kind RegistryKind
	// Host is the registry host, e.g. 123456789012.dkr.ecr.ap-northeast-1.amazonaws.com.
	Host string
	// Repository is the repository path inside the registry, e.g. apps/samples/app.
	Repository string
	// Tag is the tag that was pushed.
	Tag string
	// Digest is the manifest digest of the pushed image. May be empty when the
	// provider does not report it.
	Digest string
	// OccurredAt is when the registry recorded the push. May be the zero time.
	OccurredAt time.Time
}

// Reference is the image the event points at.
func (e ImagePushEvent) Reference() ImageReference {
	return ImageReference{
		Kind:       e.Kind,
		Host:       e.Host,
		Repository: e.Repository,
		Tag:        e.Tag,
	}
}

// LogValue keeps the event to a single grouped attribute in logs.
func (e ImagePushEvent) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("registry.kind", string(e.Kind)),
		slog.String("registry.host", e.Host),
		slog.String("image.repository", e.Repository),
		slog.String("image.tag", e.Tag),
		slog.String("image.digest", e.Digest),
	)
}
