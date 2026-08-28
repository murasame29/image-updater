// Package config reads the application configuration from the environment.
//
// The result is a value that is injected through the container. There is no
// package level configuration, so a test can build whatever configuration it
// needs without touching global state.
package config

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"

	"github.com/murasame29/image-updater/internal/model"
)

// LogLevel is the configured verbosity.
type LogLevel string

// The log levels accepted in LOG_LEVEL.
const (
	LogLevelDebug LogLevel = "debug"
	LogLevelInfo  LogLevel = "info"
	LogLevelWarn  LogLevel = "warn"
	LogLevelError LogLevel = "error"
)

// ToSlog maps the configured level onto slog. An unknown value falls back to
// info rather than failing the process over a typo.
func (l LogLevel) ToSlog() slog.Level {
	switch l {
	case LogLevelDebug:
		return slog.LevelDebug
	case LogLevelInfo:
		return slog.LevelInfo
	case LogLevelWarn:
		return slog.LevelWarn
	case LogLevelError:
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// Config is the whole application configuration, grouped by concern.
type Config struct {
	App    App
	GitHub GitHub
	AWS    AWS
}

// App holds the process wide settings.
type App struct {
	// LogLevel is the slog verbosity.
	LogLevel LogLevel `env:"LOG_LEVEL" envDefault:"info"`
	// RulePath is the rule file mapping registry repositories onto manifests.
	RulePath string `env:"CONFIG_PATH,required"`
	// PollInterval is an extra pause between two polls of the event source.
	// Long polling already blocks, so this only slows things down on purpose.
	PollInterval time.Duration `env:"INTERVAL" envDefault:"10s"`
	// Concurrency is how many events are handled at the same time.
	Concurrency int `env:"CONCURRENCY" envDefault:"10"`
	// ShutdownTimeout caps how long a shutdown waits for in-flight work.
	ShutdownTimeout time.Duration `env:"SHUTDOWN_TIMEOUT" envDefault:"30s"`
	// WorkDir is where repositories are cloned. Empty means the system temp dir.
	WorkDir string `env:"WORK_DIR"`
	// ImageLabelAnnotation turns the label driven annotation on: the pull request
	// description, the assignee and reviewer, and the well-known image manifest
	// comment block are all filled from the labels baked into the pushed image.
	//
	// Off by default. It needs the build pipeline to attach the labels and read
	// access to the registry, and neither can be assumed.
	ImageLabelAnnotation bool `env:"IMAGE_LABEL_ANNOTATION_ENABLED" envDefault:"false"`
	// ImageManifestIndent is the indent of the YAML inside the managed metadata
	// comment block. It only affects that block: the rest of the manifest is
	// edited line by line and keeps the style it already had.
	ImageManifestIndent int `env:"IMAGE_MANIFEST_INDENT" envDefault:"2"`
}

// GitHub holds the credentials of the GitHub App the updater acts as.
type GitHub struct {
	ApplicationID  int64  `env:"GITHUB_APPLICATION_ID,required"`
	InstallationID int64  `env:"GITHUB_INSTALLATION_ID,required"`
	Username       string `env:"GITHUB_USERNAME,required"`
	PrivateKeyPath string `env:"GITHUB_CRT_PATH,required"`
	// AuthorEmail signs the commits. Empty means the GitHub noreply address of
	// Username.
	AuthorEmail string `env:"GITHUB_AUTHOR_EMAIL"`
	// BaseBranch is the branch pull requests target.
	BaseBranch string `env:"GITHUB_BASE_BRANCH" envDefault:"main"`
}

// AWS holds the settings of the AWS backed adapters.
type AWS struct {
	// QueueURL is the SQS queue carrying the registry push events.
	QueueURL string `env:"AWS_QUEUE_URI,required"`
	// VisibilityTimeout is how long a received message stays hidden, in seconds.
	VisibilityTimeout int32 `env:"AWS_QUEUE_VISIBILITY_TIMEOUT" envDefault:"20"`
	// WaitTime is the SQS long polling wait, in seconds.
	WaitTime int32 `env:"AWS_QUEUE_WAIT_TIME" envDefault:"20"`
	// MaxMessages is how many messages one receive may return.
	MaxMessages int32 `env:"AWS_QUEUE_MAX_MESSAGES" envDefault:"10"`
}

// Load reads the configuration, failing on a missing required value so a
// misconfigured deployment stops at startup instead of misbehaving later.
func Load() (Config, error) {
	var cfg Config

	if err := env.Parse(&cfg); err != nil {
		return Config{}, fmt.Errorf("failed to read the configuration: %w", err)
	}

	if err := cfg.validate(); err != nil {
		return Config{}, fmt.Errorf("failed to read the configuration: %w", err)
	}

	return cfg, nil
}

// validate rejects a required value that is present but blank, which the env
// tags alone do not catch: an empty variable reads as absent to the parser and
// leaves the field at its zero value.
func (c Config) validate() error {
	missing := make([]string, 0, 6)

	if strings.TrimSpace(c.App.RulePath) == "" {
		missing = append(missing, "CONFIG_PATH")
	}
	if c.GitHub.ApplicationID == 0 {
		missing = append(missing, "GITHUB_APPLICATION_ID")
	}
	if c.GitHub.InstallationID == 0 {
		missing = append(missing, "GITHUB_INSTALLATION_ID")
	}
	if strings.TrimSpace(c.GitHub.Username) == "" {
		missing = append(missing, "GITHUB_USERNAME")
	}
	if strings.TrimSpace(c.GitHub.PrivateKeyPath) == "" {
		missing = append(missing, "GITHUB_CRT_PATH")
	}
	if strings.TrimSpace(c.AWS.QueueURL) == "" {
		missing = append(missing, "AWS_QUEUE_URI")
	}

	if len(missing) > 0 {
		return fmt.Errorf("these variables are required but empty: %s", strings.Join(missing, ", "))
	}

	// The YAML emitter silently falls back to two spaces outside this range, so
	// an unusable value is reported instead of quietly ignored.
	if !model.IsValidImageManifestIndent(c.App.ImageManifestIndent) {
		return fmt.Errorf("IMAGE_MANIFEST_INDENT has to be between %d and %d, got %d",
			model.ImageManifestIndentMin, model.ImageManifestIndentMax, c.App.ImageManifestIndent)
	}

	return nil
}
