package registry

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/murasame29/image-updater/internal/model"
)

// stubResolver records that it was asked.
type stubResolver struct {
	called bool
}

func (s *stubResolver) Resolve(context.Context, model.ImageReference) (model.ImageMetadata, error) {
	s.called = true
	return model.ImageMetadata{URI: "resolved"}, nil
}

func TestRouter_Resolve(t *testing.T) {
	t.Parallel()

	resolver := &stubResolver{}
	router := NewRouter(map[model.RegistryKind]model.MetadataResolver{model.RegistryECR: resolver})

	metadata, err := router.Resolve(context.Background(), model.ImageReference{Kind: model.RegistryECR})
	require.NoError(t, err)
	assert.Equal(t, "resolved", metadata.URI)
	assert.True(t, resolver.called)
}

func TestRouter_ResolveRejectsAnUnknownKind(t *testing.T) {
	t.Parallel()

	router := NewRouter(map[model.RegistryKind]model.MetadataResolver{model.RegistryECR: &stubResolver{}})

	_, err := router.Resolve(context.Background(), model.ImageReference{Kind: "harbor"})
	require.ErrorIs(t, err, ErrUnsupportedRegistry)
}

func TestRouter_Kinds(t *testing.T) {
	t.Parallel()

	router := NewRouter(map[model.RegistryKind]model.MetadataResolver{
		model.RegistryECR: &stubResolver{},
		"harbor":          &stubResolver{},
	})

	assert.Equal(t, []model.RegistryKind{model.RegistryECR, "harbor"}, router.Kinds())
}
