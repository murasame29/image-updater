// Package lifecycle runs the long lived components of a process and shuts them
// down together.
package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"
)

// Application is a long lived component of the process.
type Application interface {
	// Name identifies the component in logs.
	Name() string
	// Run blocks until ctx is cancelled or the component fails.
	Run(ctx context.Context) error
	// Shutdown releases the resources of the component.
	Shutdown(ctx context.Context) error
}

// Run starts every application, waits for SIGINT or SIGTERM, then shuts them
// down within timeout.
//
// Every goroutine belongs to the group, so nothing is left running behind a
// returned error, and the first failing component brings the others down with it.
//
// Returns:
//
//	nil on a clean shutdown, or the error that ended it.
func Run(ctx context.Context, timeout time.Duration, apps ...Application) error {
	if len(apps) == 0 {
		return errors.New("lifecycle: no application to run")
	}

	signalCtx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	group, groupCtx := errgroup.WithContext(signalCtx)

	for _, app := range apps {
		group.Go(func() error {
			slog.InfoContext(groupCtx, "starting", slog.String("component", app.Name()))
			if err := app.Run(groupCtx); err != nil {
				return fmt.Errorf("%s: %w", app.Name(), err)
			}
			return nil
		})
	}

	runErr := group.Wait()

	// The run context is already cancelled by now, so the shutdown gets a
	// context of its own to give in-flight work a chance to finish.
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()

	shutdownErr := shutdown(shutdownCtx, apps)

	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		return runErr
	}

	if shutdownErr != nil {
		return shutdownErr
	}

	slog.InfoContext(ctx, "shutdown completed")
	return nil
}

func shutdown(ctx context.Context, apps []Application) error {
	group := &errgroup.Group{}

	for _, app := range apps {
		group.Go(func() error {
			if err := app.Shutdown(ctx); err != nil {
				return fmt.Errorf("%s: %w", app.Name(), err)
			}
			return nil
		})
	}

	return group.Wait()
}
