package model

import "log/slog"

// RegistryKind identifies the container registry an image lives in.
//
// Every registry adapter declares its own kind, and only the metadata resolver
// registered for that kind is asked about its images. Supporting another
// registry means adding a constant here plus the adapter that produces it.
type RegistryKind string

// RegistryECR is Amazon Elastic Container Registry.
const RegistryECR RegistryKind = "ecr"

// ImageReference points at a single tagged image in a registry.
//
// Host and Repository are kept apart because the OCI distribution API needs
// them separately (`https://{host}/v2/{repository}/manifests/{tag}`) while
// kustomize matches on the two joined together.
type ImageReference struct {
	// Kind is the registry the image lives in.
	Kind RegistryKind
	// Host is the registry host, e.g. 123456789012.dkr.ecr.ap-northeast-1.amazonaws.com.
	Host string
	// Repository is the repository path inside the registry, e.g. apps/samples/app.
	Repository string
	// Tag is the image tag.
	Tag string
}

// Name is the image without its tag. It is the value that has to match
// `images[].name` in a kustomization.yaml.
func (r ImageReference) Name() string {
	if r.Host == "" {
		return r.Repository
	}
	return r.Host + "/" + r.Repository
}

// String is the fully qualified image, tag included.
func (r ImageReference) String() string {
	if r.Tag == "" {
		return r.Name()
	}
	return r.Name() + ":" + r.Tag
}

// LogValue keeps the reference to a single grouped attribute in logs.
func (r ImageReference) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("registry.kind", string(r.Kind)),
		slog.String("registry.host", r.Host),
		slog.String("image.repository", r.Repository),
		slog.String("image.tag", r.Tag),
	)
}

// ImageMetadata is what a registry can tell about an image that was pushed.
type ImageMetadata struct {
	// Labels are the OCI config labels baked into the image.
	Labels map[string]string
	// URI is the fully qualified image reference, tag included.
	URI string
	// SizeBytes is the total size of the image layers.
	SizeBytes int64
}

// ImageLabels reads the well-known labels out of the raw metadata.
func (m ImageMetadata) ImageLabels() ImageLabels {
	labels := NewImageLabels(m.Labels)
	labels.ImageURI = m.URI
	labels.ImageSizeBytes = m.SizeBytes
	return labels
}
