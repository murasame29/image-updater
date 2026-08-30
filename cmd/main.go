// Command image-updater watches a container registry for image pushes and
// opens a pull request that bumps the image tag in the deployment manifests.
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/murasame29/image-updater/internal/config"
	"github.com/murasame29/image-updater/internal/container"
	"github.com/murasame29/image-updater/pkg/lifecycle"
)

func main() {
	if err := run(); err != nil {
		slog.Error("execution failed", slog.String("error.message", err.Error()))
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		AddSource: true,
		Level:     cfg.App.LogLevel.ToSlog(),
	})))

	ctx := context.Background()

	worker, err := container.BuildWorker(ctx, cfg)
	if err != nil {
		return err
	}

	return lifecycle.Run(ctx, cfg.App.ShutdownTimeout, worker)
}
