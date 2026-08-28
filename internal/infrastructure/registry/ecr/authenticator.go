package ecr

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"

	"github.com/murasame29/image-updater/internal/model"
)

const (
	// tokenAPITimeout caps the token exchange.
	tokenAPITimeout = 30 * time.Second

	// tokenSkew refreshes the token before it actually expires, so a request
	// that starts just before expiry still carries a valid token.
	tokenSkew = 5 * time.Minute

	// tokenFallbackTTL is used when the API does not report an expiry. ECR
	// tokens live for 12 hours, so this is comfortably inside that.
	tokenFallbackTTL = time.Hour
)

// TokenAPI is the ECR call the authenticator needs.
type TokenAPI interface {
	GetAuthorizationToken(ctx context.Context, params *ecr.GetAuthorizationTokenInput, optFns ...func(*ecr.Options)) (*ecr.GetAuthorizationTokenOutput, error)
}

// Authenticator exchanges the ambient AWS credentials for an ECR authorization
// token and reuses it until it is close to expiry, so a burst of pushes does not
// turn into a burst of GetAuthorizationToken calls.
type Authenticator struct {
	api TokenAPI
	now func() time.Time

	mu        sync.Mutex
	token     string
	expiresAt time.Time
}

// NewAuthenticator builds an authenticator on top of api.
func NewAuthenticator(api TokenAPI) *Authenticator {
	return &Authenticator{api: api, now: time.Now}
}

// AuthHeader returns the Authorization header value for any repository of the
// registry. ECR scopes the token by IAM, not by repository, so the reference is
// not used.
func (a *Authenticator) AuthHeader(ctx context.Context, _ model.ImageReference) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.token != "" && a.now().Before(a.expiresAt) {
		return basic(a.token), nil
	}

	ctx, cancel := context.WithTimeout(ctx, tokenAPITimeout)
	defer cancel()

	output, err := a.api.GetAuthorizationToken(ctx, &ecr.GetAuthorizationTokenInput{})
	if err != nil {
		return "", fmt.Errorf("failed to get an ECR authorization token: %w", err)
	}

	if len(output.AuthorizationData) == 0 || output.AuthorizationData[0].AuthorizationToken == nil {
		return "", fmt.Errorf("ECR returned no authorization data")
	}

	a.token = aws.ToString(output.AuthorizationData[0].AuthorizationToken)
	a.expiresAt = a.now().Add(tokenFallbackTTL)
	if expiry := output.AuthorizationData[0].ExpiresAt; expiry != nil {
		a.expiresAt = expiry.Add(-tokenSkew)
	}

	return basic(a.token), nil
}

// basic builds the header value. The ECR token is already the base64 of
// "AWS:<password>", which is exactly what basic auth expects.
func basic(token string) string { return "Basic " + token }
