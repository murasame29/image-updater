package config

import (
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// required is the environment a healthy deployment provides.
var required = map[string]string{
	"CONFIG_PATH":            "/etc/image-updater/config/config.yaml",
	"GITHUB_APPLICATION_ID":  "1",
	"GITHUB_INSTALLATION_ID": "2",
	"GITHUB_USERNAME":        "image-updater",
	"GITHUB_CRT_PATH":        "/etc/image-updater/github-app-private-key/githubAppPrivateKey",
	"AWS_QUEUE_URI":          "https://sqs.ap-northeast-1.amazonaws.com/123456789012/image-updater-queue",
}

func setEnv(t *testing.T, env map[string]string) {
	t.Helper()
	for key, value := range env {
		t.Setenv(key, value)
	}
}

func TestLoad(t *testing.T) {
	setEnv(t, required)

	cfg, err := Load()
	require.NoError(t, err)

	assert.Equal(t, "/etc/image-updater/config/config.yaml", cfg.App.RulePath)
	assert.Equal(t, int64(1), cfg.GitHub.ApplicationID)
	assert.Equal(t, int64(2), cfg.GitHub.InstallationID)
	assert.Equal(t, "image-updater", cfg.GitHub.Username)
	assert.Equal(t, required["AWS_QUEUE_URI"], cfg.AWS.QueueURL)

	// The defaults have to keep the deployed manifests working unchanged.
	assert.Equal(t, LogLevelInfo, cfg.App.LogLevel)
	assert.Equal(t, 10*time.Second, cfg.App.PollInterval)
	assert.Equal(t, 10, cfg.App.Concurrency)
	assert.Equal(t, 30*time.Second, cfg.App.ShutdownTimeout)
	assert.Equal(t, "main", cfg.GitHub.BaseBranch)
	assert.Equal(t, int32(20), cfg.AWS.VisibilityTimeout)
	assert.Equal(t, int32(20), cfg.AWS.WaitTime)
	assert.Equal(t, int32(10), cfg.AWS.MaxMessages)
}

func TestLoadRejectsAMissingRequiredValue(t *testing.T) {
	for missing := range required {
		t.Run(missing, func(t *testing.T) {
			for key, value := range required {
				if key == missing {
					continue
				}
				t.Setenv(key, value)
			}
			t.Setenv(missing, "")

			_, err := Load()
			require.Error(t, err, "%s has to be required", missing)
		})
	}
}

func TestLogLevelToSlog(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		level LogLevel
		want  slog.Level
	}{
		{name: "debug", level: LogLevelDebug, want: slog.LevelDebug},
		{name: "info", level: LogLevelInfo, want: slog.LevelInfo},
		{name: "warn", level: LogLevelWarn, want: slog.LevelWarn},
		{name: "error", level: LogLevelError, want: slog.LevelError},
		{name: "未知の値は info に倒す", level: "verbose", want: slog.LevelInfo},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, tt.level.ToSlog())
		})
	}
}
