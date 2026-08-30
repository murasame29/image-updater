package config

import (
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/murasame29/image-updater/internal/model"
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
	assert.False(t, cfg.App.ImageLabelAnnotation, "label 由来の注釈は opt-in")
	assert.Equal(t, model.ImageManifestIndentDefault, cfg.App.ImageManifestIndent)
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

func TestLoadReadsTheImageLabelAnnotationFlag(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "true で有効", value: "true", want: true},
		{name: "false で無効", value: "false", want: false},
		{name: "1 で有効", value: "1", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setEnv(t, required)
			t.Setenv("IMAGE_LABEL_ANNOTATION_ENABLED", tt.value)

			cfg, err := Load()
			require.NoError(t, err)
			assert.Equal(t, tt.want, cfg.App.ImageLabelAnnotation)
		})
	}
}

func TestLoadValidatesTheImageManifestIndent(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    int
		wantErr bool
	}{
		{name: "既定値", value: "", want: model.ImageManifestIndentDefault},
		{name: "下限", value: "2", want: 2},
		{name: "上限", value: "9", want: 9},
		{name: "4 スペース", value: "4", want: 4},
		// Reject values that the YAML emitter would silently reset to 2 so the
		// misconfiguration is visible at startup.
		{name: "下限未満は拒否", value: "1", wantErr: true},
		{name: "上限超過は拒否", value: "10", wantErr: true},
		{name: "0 は拒否", value: "0", wantErr: true},
		{name: "負値は拒否", value: "-2", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setEnv(t, required)
			if tt.value != "" {
				t.Setenv("IMAGE_MANIFEST_INDENT", tt.value)
			}

			cfg, err := Load()
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "IMAGE_MANIFEST_INDENT")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, cfg.App.ImageManifestIndent)
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
