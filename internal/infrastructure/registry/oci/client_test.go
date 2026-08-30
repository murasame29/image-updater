package oci

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/murasame29/image-updater/internal/model"
)

const (
	configDigest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	imageDigest  = "sha256:2222222222222222222222222222222222222222222222222222222222222222"

	configBlob = `{"config":{"Labels":{` +
		`"org.opencontainers.image.source":"https://github.com/example-org/example-ci",` +
		`"org.opencontainers.image.revision":"abc1234"}}}`
)

// staticAuth is a fake Authenticator.
type staticAuth struct {
	header string
	err    error
}

func (a staticAuth) AuthHeader(context.Context, model.ImageReference) (string, error) {
	return a.header, a.err
}

// newTestClient serves the given handler and points a client at it.
func newTestClient(t *testing.T, auth Authenticator, handler http.HandlerFunc) (*Client, string) {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	client := NewClient(auth,
		WithHTTPClient(server.Client()),
		WithScheme("http"),
	)

	return client, strings.TrimPrefix(server.URL, "http://")
}

func TestClient_Resolve(t *testing.T) {
	t.Parallel()

	manifestBody := fmt.Sprintf(
		`{"mediaType":%q,"config":{"digest":%q,"size":1000},"layers":[{"size":120},{"size":80}]}`,
		mediaTypeOCIManifest, configDigest)

	var requests []string

	client, host := newTestClient(t, staticAuth{header: "Basic dGVzdC10b2tlbg=="}, func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.Path)
		assert.Equal(t, "Basic dGVzdC10b2tlbg==", r.Header.Get("Authorization"))

		switch {
		case strings.HasSuffix(r.URL.Path, "/manifests/abc1234"):
			assert.Contains(t, r.Header.Get("Accept"), mediaTypeOCIManifest)
			fmt.Fprint(w, manifestBody)
		case strings.HasSuffix(r.URL.Path, "/blobs/"+configDigest):
			fmt.Fprint(w, configBlob)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	metadata, err := client.Resolve(context.Background(), model.ImageReference{
		Kind:       model.RegistryECR,
		Host:       host,
		Repository: "apps/samples/app",
		Tag:        "abc1234",
	})
	require.NoError(t, err)

	assert.Equal(t, "https://github.com/example-org/example-ci", metadata.Labels["org.opencontainers.image.source"])
	assert.Equal(t, "abc1234", metadata.Labels["org.opencontainers.image.revision"])
	assert.Equal(t, host+"/apps/samples/app:abc1234", metadata.URI)
	assert.Equal(t, int64(200), metadata.SizeBytes, "the size is the sum of the layer sizes")

	assert.Equal(t, []string{
		"/v2/apps/samples/app/manifests/abc1234",
		"/v2/apps/samples/app/blobs/" + configDigest,
	}, requests)
}

func TestClient_ResolveFollowsAnImageIndex(t *testing.T) {
	t.Parallel()

	indexBody := fmt.Sprintf(`{"mediaType":%q,"manifests":[
		{"digest":"sha256:9999999999999999999999999999999999999999999999999999999999999999","platform":{"os":"unknown","architecture":"unknown"}},
		{"digest":%q,"platform":{"os":"linux","architecture":"amd64"}}
	]}`, mediaTypeOCIIndex, imageDigest)

	manifestBody := fmt.Sprintf(`{"mediaType":%q,"config":{"digest":%q},"layers":[{"size":42}]}`, mediaTypeOCIManifest, configDigest)

	client, host := newTestClient(t, staticAuth{header: "Bearer token"}, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/manifests/latest"):
			fmt.Fprint(w, indexBody)
		case strings.HasSuffix(r.URL.Path, "/manifests/"+imageDigest):
			fmt.Fprint(w, manifestBody)
		case strings.HasSuffix(r.URL.Path, "/blobs/"+configDigest):
			fmt.Fprint(w, configBlob)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	metadata, err := client.Resolve(context.Background(), model.ImageReference{
		Host:       host,
		Repository: "example-org/app",
		Tag:        "latest",
	})
	require.NoError(t, err)

	assert.Equal(t, "abc1234", metadata.Labels["org.opencontainers.image.revision"])
	assert.Equal(t, int64(42), metadata.SizeBytes)
}

func TestClient_ResolveFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		auth    Authenticator
		handler http.HandlerFunc
		ref     func(host string) model.ImageReference
		wantErr string
	}{
		{
			name: "認証に失敗したら中断する",
			auth: staticAuth{err: errors.New("no credentials")},
			handler: func(w http.ResponseWriter, _ *http.Request) {
				t.Error("the registry must not be called without an Authorization header")
				w.WriteHeader(http.StatusOK)
			},
			wantErr: "failed to authenticate",
		},
		{
			name:    "マニフェストが 404 ならエラー",
			auth:    staticAuth{header: "Basic x"},
			handler: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) },
			wantErr: "unexpected status code: 404",
		},
		{
			name: "マニフェストが JSON でなければエラー",
			auth: staticAuth{header: "Basic x"},
			handler: func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(w, "not json")
			},
			wantErr: "failed to parse the manifest",
		},
		{
			name: "config ディスクリプタがなければエラー",
			auth: staticAuth{header: "Basic x"},
			handler: func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(w, `{"layers":[]}`)
			},
			wantErr: "carries no config descriptor",
		},
		{
			name:    "リポジトリ名にパストラバーサルがあれば拒否",
			auth:    staticAuth{header: "Basic x"},
			handler: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) },
			ref: func(host string) model.ImageReference {
				return model.ImageReference{Host: host, Repository: "apps/../../etc", Tag: "abc1234"}
			},
			wantErr: "path traversal",
		},
		{
			name:    "タグに不正な文字があれば拒否",
			auth:    staticAuth{header: "Basic x"},
			handler: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) },
			ref: func(host string) model.ImageReference {
				return model.ImageReference{Host: host, Repository: "apps/app", Tag: "abc/../1234"}
			},
			wantErr: "unsupported character",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client, host := newTestClient(t, tt.auth, tt.handler)

			ref := model.ImageReference{Host: host, Repository: "apps/app", Tag: "abc1234"}
			if tt.ref != nil {
				ref = tt.ref(host)
			}

			_, err := client.Resolve(context.Background(), ref)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestManifestImageDigest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		manifest   manifest
		wantDigest string
		wantIndex  bool
	}{
		{
			name:     "イメージマニフェストはそのまま読む",
			manifest: manifest{Config: descriptor{Digest: configDigest}},
		},
		{
			name: "linux/amd64 を優先する",
			manifest: manifest{Manifests: []descriptor{
				{Digest: "sha256:aaa", Platform: &platform{OS: "linux", Architecture: "arm64"}},
				{Digest: "sha256:bbb", Platform: &platform{OS: "linux", Architecture: "amd64"}},
			}},
			wantDigest: "sha256:bbb",
			wantIndex:  true,
		},
		{
			name: "attestation マニフェストは飛ばす",
			manifest: manifest{Manifests: []descriptor{
				{Digest: "sha256:aaa", Platform: &platform{OS: unknownPlatform, Architecture: unknownPlatform}},
				{Digest: "sha256:bbb", Platform: &platform{OS: "linux", Architecture: "arm64"}},
			}},
			wantDigest: "sha256:bbb",
			wantIndex:  true,
		},
		{
			name:      "読めるマニフェストがなければ index として扱わない",
			manifest:  manifest{Manifests: []descriptor{{Digest: "sha256:aaa", Platform: &platform{OS: unknownPlatform, Architecture: unknownPlatform}}}},
			wantIndex: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			digest, isIndex := tt.manifest.imageDigest()
			assert.Equal(t, tt.wantIndex, isIndex)
			assert.Equal(t, tt.wantDigest, digest)
		})
	}
}
