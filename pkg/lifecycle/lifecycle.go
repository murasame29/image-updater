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
	runFailures := make(chan error, len(apps))

	for _, app := range apps {
		group.Go(func() error {
			slog.InfoContext(groupCtx, "starting", slog.String("component", app.Name()))
			if err := app.Run(groupCtx); err != nil {
				wrapped := fmt.Errorf("%s: %w", app.Name(), err)
				// Publish before returning: errgroup only exposes the error after every
				// sibling exits, which is too late to start the shutdown deadline.
				runFailures <- wrapped
				return wrapped
			}
			return nil
		})
	}

	runDone := make(chan error, 1)
	go func() { runDone <- group.Wait() }()

	var runErr error
	waitForRun := false
	select {
	case runErr = <-runDone:
	case runErr = <-runFailures:
		waitForRun = true
	case <-signalCtx.Done():
		waitForRun = true
	}

	// The timeout starts as soon as shutdown begins and bounds both Run draining
	// and Shutdown, regardless of which one caused the lifecycle to stop.
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()

	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- shutdown(shutdownCtx, apps) }()

	var shutdownErr error
	var runCh <-chan error
	if waitForRun {
		runCh = runDone
	}
	shutdownCh := (<-chan error)(shutdownDone)

	for runCh != nil || shutdownCh != nil {
		select {
		case runErr = <-runCh:
			runCh = nil
		case shutdownErr = <-shutdownCh:
			shutdownCh = nil
		case <-shutdownCtx.Done():
			timeoutErr := fmt.Errorf("lifecycle: shutdown did not complete within %s: %w", timeout, shutdownCtx.Err())
			if errors.Is(runErr, context.Canceled) {
				runErr = nil
			}
			return errors.Join(runErr, shutdownErr, timeoutErr)
		}
	}

	return finish(ctx, runErr, shutdownErr)
}

func finish(ctx context.Context, runErr, shutdownErr error) error {
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
