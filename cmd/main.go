// Command image-updater watches a container registry for image pushes and
// opens a pull request that bumps the image tag in the deployment manifests.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/murasame29/image-updater/internal/config"
	"github.com/murasame29/image-updater/internal/container"
	"github.com/murasame29/image-updater/internal/version"
	"github.com/murasame29/image-updater/pkg/lifecycle"
)

func main() {
	ctx := context.Background()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		slog.ErrorContext(ctx, "execution failed", slog.String("error.message", err.Error()))
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("image-updater", flag.ContinueOnError)
	flags.SetOutput(stdout)
	showVersion := flags.Bool("version", false, "print build version and exit")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return fmt.Errorf("parse command arguments: %w", err)
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected command arguments: %s", strings.Join(flags.Args(), " "))
	}
	if *showVersion {
		if _, err := fmt.Fprintln(stdout, version.String()); err != nil {
			return fmt.Errorf("write version: %w", err)
		}
		return nil
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		AddSource: true,
		Level:     cfg.App.LogLevel.ToSlog(),
	})))
	slog.InfoContext(ctx, "starting image updater",
		slog.String("service.version", version.Version),
		slog.String("vcs.revision", version.Commit),
		slog.String("build.date", version.BuildDate),
	)

	worker, err := container.BuildWorker(ctx, cfg)
	if err != nil {
		return err
	}

	return lifecycle.Run(ctx, cfg.App.ShutdownTimeout, worker)
}
