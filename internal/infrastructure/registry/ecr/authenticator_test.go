package ecr

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/aws/aws-sdk-go-v2/service/ecr/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/murasame29/image-updater/internal/model"
)

// fakeTokenAPI counts the calls so the caching can be observed.
type fakeTokenAPI struct {
	calls     int
	token     string
	expiresAt *time.Time
	err       error
}

func (f *fakeTokenAPI) GetAuthorizationToken(context.Context, *ecr.GetAuthorizationTokenInput, ...func(*ecr.Options)) (*ecr.GetAuthorizationTokenOutput, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	if f.token == "" {
		return &ecr.GetAuthorizationTokenOutput{}, nil
	}

	return &ecr.GetAuthorizationTokenOutput{
		AuthorizationData: []types.AuthorizationData{
			{AuthorizationToken: aws.String(f.token), ExpiresAt: f.expiresAt},
		},
	}, nil
}

func TestAuthenticator_AuthHeader(t *testing.T) {
	t.Parallel()

	expiry := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	api := &fakeTokenAPI{token: "QVdTOnBhc3N3b3Jk", expiresAt: &expiry}

	now := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	auth := NewAuthenticator(api)
	auth.now = func() time.Time { return now }

	header, err := auth.AuthHeader(context.Background(), model.ImageReference{})
	require.NoError(t, err)
	assert.Equal(t, "Basic QVdTOnBhc3N3b3Jk", header)
	assert.Equal(t, 1, api.calls)

	// A second lookup inside the validity window reuses the token.
	_, err = auth.AuthHeader(context.Background(), model.ImageReference{})
	require.NoError(t, err)
	assert.Equal(t, 1, api.calls, "the cached token has to be reused")

	// Past the expiry minus the safety margin, a new token is fetched.
	now = expiry.Add(-tokenSkew)
	_, err = auth.AuthHeader(context.Background(), model.ImageReference{})
	require.NoError(t, err)
	assert.Equal(t, 2, api.calls, "an expiring token has to be refreshed")
}

func TestAuthenticator_AuthHeaderFallsBackToATTL(t *testing.T) {
	t.Parallel()

	api := &fakeTokenAPI{token: "dG9rZW4="}

	now := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	auth := NewAuthenticator(api)
	auth.now = func() time.Time { return now }

	_, err := auth.AuthHeader(context.Background(), model.ImageReference{})
	require.NoError(t, err)

	now = now.Add(tokenFallbackTTL)
	_, err = auth.AuthHeader(context.Background(), model.ImageReference{})
	require.NoError(t, err)
	assert.Equal(t, 2, api.calls)
}

func TestAuthenticator_AuthHeaderFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		api     *fakeTokenAPI
		wantErr string
	}{
		{
			name:    "API がエラーを返したら伝播する",
			api:     &fakeTokenAPI{err: errors.New("access denied")},
			wantErr: "failed to get an ECR authorization token",
		},
		{
			name:    "認証データが空ならエラー",
			api:     &fakeTokenAPI{},
			wantErr: "no authorization data",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := NewAuthenticator(tt.api).AuthHeader(context.Background(), model.ImageReference{})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}
