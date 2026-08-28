package github

import (
	"errors"
	"fmt"
	"testing"

	gogit "github.com/go-git/go-git/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsNonFastForward(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "センチネルそのもの",
			err:  gogit.ErrNonFastForwardUpdate,
			want: true,
		},
		{
			name: "%w でラップされている場合",
			err:  fmt.Errorf("push failed: %w", gogit.ErrNonFastForwardUpdate),
			want: true,
		},
		{
			// go-git はセンチネルをラップせずメッセージに埋め込むことがある。
			name: "メッセージに埋め込まれている場合",
			err:  errors.New(gogit.ErrNonFastForwardUpdate.Error() + ": refs/heads/x"),
			want: true,
		},
		{
			name: "無関係なエラー",
			err:  errors.New("authentication required"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, isNonFastForward(tt.err))
		})
	}
}

func TestNonEmpty(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{"a", "b"}, nonEmpty([]string{"a", "", "b"}))
	assert.Empty(t, nonEmpty(nil))
	assert.Empty(t, nonEmpty([]string{""}))
}

func TestNewRepositoryValidatesItsConfiguration(t *testing.T) {
	t.Parallel()

	valid := Config{
		ApplicationID:  1,
		InstallationID: 2,
		Username:       "image-updater",
		PrivateKeyPath: "testdata/missing-key.pem",
	}

	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "application ID が未設定", mutate: func(c *Config) { c.ApplicationID = 0 }},
		{name: "installation ID が未設定", mutate: func(c *Config) { c.InstallationID = 0 }},
		{name: "username が未設定", mutate: func(c *Config) { c.Username = "" }},
		{name: "秘密鍵のパスが未設定", mutate: func(c *Config) { c.PrivateKeyPath = "" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := valid
			tt.mutate(&cfg)

			_, err := NewRepository(cfg)
			require.Error(t, err)
		})
	}
}

func TestNewRepositoryReportsAnUnreadableKey(t *testing.T) {
	t.Parallel()

	_, err := NewRepository(Config{
		ApplicationID:  1,
		InstallationID: 2,
		Username:       "image-updater",
		PrivateKeyPath: "testdata/missing-key.pem",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "private key")
}
