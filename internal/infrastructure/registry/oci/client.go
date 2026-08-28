// Package oci reads image metadata over the OCI distribution API.
//
// Every registry this app has to support (ECR today, Artifact Registry, Harbor
// and GHCR later) serves the same `/v2/` API. What differs is authentication, so
// that is the only part injected: a registry adapter provides an Authenticator
// and reuses this client for the rest.
package oci

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/murasame29/image-updater/internal/model"
)

const (
	mediaTypeDockerManifest = "application/vnd.docker.distribution.manifest.v2+json"
	mediaTypeDockerIndex    = "application/vnd.docker.distribution.manifest.list.v2+json"
	mediaTypeOCIManifest    = "application/vnd.oci.image.manifest.v1+json"
	mediaTypeOCIIndex       = "application/vnd.oci.image.index.v1+json"

	// unknownPlatform marks the attestation manifests buildx adds to an index.
	// They carry no image config, so they are never the manifest to read.
	unknownPlatform = "unknown"

	preferredOS           = "linux"
	preferredArchitecture = "amd64"

	defaultScheme  = "https"
	defaultTimeout = 30 * time.Second

	// maxBlobSize caps the config blob read. An image config is a few KiB; the
	// limit only keeps a misbehaving registry from exhausting memory.
	maxBlobSize = 4 << 20
)

// Authenticator supplies the value of the Authorization header for a registry.
//
// ECR exchanges IAM credentials for a basic token, Harbor uses a robot account,
// GHCR and Artifact Registry use bearer tokens.
type Authenticator interface {
	AuthHeader(ctx context.Context, ref model.ImageReference) (string, error)
}

// Client resolves image metadata against an OCI distribution registry.
type Client struct {
	auth       Authenticator
	httpClient *http.Client
	scheme     string
	timeout    time.Duration
}

// Option tunes a Client.
type Option func(*Client)

// WithHTTPClient replaces the HTTP client used for registry calls.
func WithHTTPClient(httpClient *http.Client) Option {
	return func(c *Client) {
		if httpClient != nil {
			c.httpClient = httpClient
		}
	}
}

// WithScheme replaces the URL scheme. Only tests should need it.
func WithScheme(scheme string) Option {
	return func(c *Client) {
		if scheme != "" {
			c.scheme = scheme
		}
	}
}

// WithTimeout replaces the per request timeout.
func WithTimeout(timeout time.Duration) Option {
	return func(c *Client) {
		if timeout > 0 {
			c.timeout = timeout
		}
	}
}

var _ model.MetadataResolver = (*Client)(nil)

// NewClient builds a client that authenticates with auth.
func NewClient(auth Authenticator, opts ...Option) *Client {
	client := &Client{
		auth:    auth,
		scheme:  defaultScheme,
		timeout: defaultTimeout,
	}

	for _, opt := range opts {
		opt(client)
	}

	if client.httpClient == nil {
		client.httpClient = &http.Client{Timeout: client.timeout}
	}

	return client
}

// Resolve reads the labels and the size of ref.
//
// A multi platform image is followed one level down to the manifest of the
// preferred platform, because only a real image manifest carries a config blob.
//
// Returns:
//
//	The metadata, or an error when the image cannot be read.
func (c *Client) Resolve(ctx context.Context, ref model.ImageReference) (model.ImageMetadata, error) {
	if err := validateReference(ref); err != nil {
		return model.ImageMetadata{}, err
	}

	header, err := c.auth.AuthHeader(ctx, ref)
	if err != nil {
		return model.ImageMetadata{}, fmt.Errorf("failed to authenticate against %s: %w", ref.Host, err)
	}

	manifest, err := c.manifest(ctx, ref, header, ref.Tag)
	if err != nil {
		return model.ImageMetadata{}, err
	}

	if digest, isIndex := manifest.imageDigest(); isIndex {
		if err := validateDigest(digest); err != nil {
			return model.ImageMetadata{}, err
		}
		slog.DebugContext(ctx, "following image index", "image", ref, "manifest.digest", digest)
		if manifest, err = c.manifest(ctx, ref, header, digest); err != nil {
			return model.ImageMetadata{}, err
		}
	}

	if manifest.Config.Digest == "" {
		return model.ImageMetadata{}, fmt.Errorf("manifest of %s carries no config descriptor", ref)
	}

	config, err := c.imageConfig(ctx, ref, header, manifest.Config.Digest)
	if err != nil {
		return model.ImageMetadata{}, err
	}

	labels := config.Config.Labels
	if labels == nil {
		labels = map[string]string{}
	}

	return model.ImageMetadata{
		Labels:    labels,
		URI:       ref.String(),
		SizeBytes: manifest.size(),
	}, nil
}

// manifest fetches the manifest of ref identified by a tag or a digest.
func (c *Client) manifest(ctx context.Context, ref model.ImageReference, authHeader, reference string) (manifest, error) {
	url := fmt.Sprintf("%s://%s/v2/%s/manifests/%s", c.scheme, ref.Host, ref.Repository, reference)

	body, err := c.get(ctx, url, authHeader, strings.Join([]string{
		mediaTypeOCIManifest,
		mediaTypeOCIIndex,
		mediaTypeDockerManifest,
		mediaTypeDockerIndex,
	}, ", "))
	if err != nil {
		return manifest{}, fmt.Errorf("failed to fetch the manifest of %s: %w", ref, err)
	}

	var parsed manifest
	if err := json.Unmarshal(body, &parsed); err != nil {
		return manifest{}, fmt.Errorf("failed to parse the manifest of %s: %w", ref, err)
	}

	return parsed, nil
}

// imageConfig fetches and parses the config blob holding the image labels.
func (c *Client) imageConfig(ctx context.Context, ref model.ImageReference, authHeader, digest string) (imageConfig, error) {
	if err := validateDigest(digest); err != nil {
		return imageConfig{}, err
	}

	url := fmt.Sprintf("%s://%s/v2/%s/blobs/%s", c.scheme, ref.Host, ref.Repository, digest)

	body, err := c.get(ctx, url, authHeader, "*/*")
	if err != nil {
		return imageConfig{}, fmt.Errorf("failed to fetch the config blob of %s: %w", ref, err)
	}

	var parsed imageConfig
	if err := json.Unmarshal(body, &parsed); err != nil {
		return imageConfig{}, fmt.Errorf("failed to parse the config blob of %s: %w", ref, err)
	}

	return parsed, nil
}

func (c *Client) get(ctx context.Context, url, authHeader, accept string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build the request: %w", err)
	}

	req.Header.Set("Authorization", authHeader)
	req.Header.Set("Accept", accept)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBlobSize))
	if err != nil {
		return nil, fmt.Errorf("failed to read the response body: %w", err)
	}

	return body, nil
}
