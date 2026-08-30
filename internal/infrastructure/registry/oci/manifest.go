package oci

import (
	"fmt"
	"strings"

	"github.com/murasame29/image-updater/internal/model"
)

// descriptor is an OCI content descriptor.
type descriptor struct {
	MediaType string    `json:"mediaType"`
	Digest    string    `json:"digest"`
	Size      int64     `json:"size"`
	Platform  *platform `json:"platform,omitempty"`
}

type platform struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
}

// manifest covers both an image manifest and an image index. A registry answers
// either shape on the same endpoint, and the two are told apart by which of
// Config and Manifests is populated.
type manifest struct {
	MediaType string       `json:"mediaType"`
	Config    descriptor   `json:"config"`
	Layers    []descriptor `json:"layers"`
	Manifests []descriptor `json:"manifests"`
}

// imageDigest returns the digest of the platform manifest to read when the
// document is an index.
//
// Returns:
//
//	The digest and true for an index, an empty string and false for an image
//	manifest that can be read directly.
func (m manifest) imageDigest() (string, bool) {
	if m.Config.Digest != "" || len(m.Manifests) == 0 {
		return "", false
	}

	var fallback string
	for _, entry := range m.Manifests {
		if entry.Digest == "" || entry.isAttestation() {
			continue
		}
		if entry.isPreferredPlatform() {
			return entry.Digest, true
		}
		if fallback == "" {
			fallback = entry.Digest
		}
	}

	return fallback, fallback != ""
}

// size is the total size of the image layers, which is what a registry reports
// as the size of the image.
func (m manifest) size() int64 {
	var total int64
	for _, layer := range m.Layers {
		total += layer.Size
	}
	return total
}

// isAttestation reports whether the descriptor is one of the attestation
// manifests buildx attaches to an index. They carry no image config.
func (d descriptor) isAttestation() bool {
	return d.Platform != nil &&
		(d.Platform.OS == unknownPlatform || d.Platform.Architecture == unknownPlatform)
}

func (d descriptor) isPreferredPlatform() bool {
	return d.Platform != nil &&
		d.Platform.OS == preferredOS &&
		d.Platform.Architecture == preferredArchitecture
}

// imageConfig is the part of an image config blob this app reads.
type imageConfig struct {
	Config struct {
		Labels map[string]string `json:"Labels"`
	} `json:"config"`
}

// validateReference rejects a reference that must not be interpolated into a
// registry URL. Repository and tag come from an external event, so they are
// checked before they reach the wire.
func validateReference(ref model.ImageReference) error {
	if ref.Host == "" {
		return fmt.Errorf("image reference has no registry host")
	}
	if err := validatePathComponent("repository", ref.Repository, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-/"); err != nil {
		return err
	}
	return validatePathComponent("tag", ref.Tag, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-")
}

// validateDigest rejects a digest that must not be interpolated into a registry
// URL. Digests come back from the registry itself, so this is defence in depth.
func validateDigest(digest string) error {
	algorithm, encoded, ok := strings.Cut(digest, ":")
	if !ok {
		return fmt.Errorf("digest %q carries no algorithm", digest)
	}
	if err := validatePathComponent("digest algorithm", algorithm, "abcdefghijklmnopqrstuvwxyz0123456789+.-_"); err != nil {
		return err
	}
	return validatePathComponent("digest", encoded, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789=_-")
}

func validatePathComponent(name, value, allowed string) error {
	if value == "" {
		return fmt.Errorf("image reference has no %s", name)
	}
	if strings.ContainsAny(value, "\x00") {
		return fmt.Errorf("image %s contains a control character", name)
	}
	for _, char := range value {
		if !strings.ContainsRune(allowed, char) {
			return fmt.Errorf("image %s %q contains the unsupported character %q", name, value, char)
		}
	}
	if strings.Contains(value, "..") {
		return fmt.Errorf("image %s %q contains a path traversal", name, value)
	}
	return nil
}
