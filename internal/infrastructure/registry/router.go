// Package registry routes an image to the resolver of its registry.
package registry

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/murasame29/image-updater/internal/model"
)

// ErrUnsupportedRegistry means no resolver is registered for the registry the
// image lives in.
var ErrUnsupportedRegistry = errors.New("unsupported registry")

// Router dispatches a metadata lookup to the resolver of the registry kind.
//
// This is the extension point for a new registry: build its resolver and
// register it under its kind. Nothing else in the application changes.
type Router struct {
	resolvers map[model.RegistryKind]model.MetadataResolver
}

var _ model.MetadataResolver = (*Router)(nil)

// NewRouter builds a router over resolvers keyed by registry kind.
func NewRouter(resolvers map[model.RegistryKind]model.MetadataResolver) *Router {
	return &Router{resolvers: maps.Clone(resolvers)}
}

// Kinds lists the registries the router can resolve, sorted for stable logging.
func (r *Router) Kinds() []model.RegistryKind {
	return slices.Sorted(maps.Keys(r.resolvers))
}

// Resolve hands ref to the resolver of its kind.
//
// Returns:
//
//	The metadata, or ErrUnsupportedRegistry when the kind has no resolver.
func (r *Router) Resolve(ctx context.Context, ref model.ImageReference) (model.ImageMetadata, error) {
	resolver, ok := r.resolvers[ref.Kind]
	if !ok {
		return model.ImageMetadata{}, fmt.Errorf("%w: %q", ErrUnsupportedRegistry, ref.Kind)
	}

	return resolver.Resolve(ctx, ref)
}
