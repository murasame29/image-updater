package lifecycle

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeApp records how it was driven.
type fakeApp struct {
	name     string
	runErr   error
	shutErr  error
	started  atomic.Bool
	stopped  atomic.Bool
	runUntil time.Duration
}

func (a *fakeApp) Name() string { return a.name }

func (a *fakeApp) Run(ctx context.Context) error {
	a.started.Store(true)

	if a.runErr != nil {
		return a.runErr
	}

	if a.runUntil > 0 {
		select {
		case <-ctx.Done():
		case <-time.After(a.runUntil):
		}
		return nil
	}

	<-ctx.Done()
	return nil
}

func (a *fakeApp) Shutdown(context.Context) error {
	a.stopped.Store(true)
	return a.shutErr
}

func TestRunStopsOnACancelledContext(t *testing.T) {
	t.Parallel()

	app := &fakeApp{name: "worker"}

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- Run(ctx, time.Second, app) }()

	require.Eventually(t, app.started.Load, time.Second, 5*time.Millisecond)
	cancel()

	select {
	case err := <-done:
		require.NoError(t, err, "a cancelled context is a clean shutdown")
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}

	assert.True(t, app.stopped.Load(), "Shutdown has to be called")
}

func TestRunPropagatesARunFailure(t *testing.T) {
	t.Parallel()

	failing := &fakeApp{name: "failing", runErr: errors.New("boom")}
	other := &fakeApp{name: "other"}

	err := Run(context.Background(), time.Second, failing, other)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failing: boom")

	// The failure has to bring the sibling down as well.
	assert.True(t, other.stopped.Load())
}

func TestRunPropagatesAShutdownFailure(t *testing.T) {
	t.Parallel()

	app := &fakeApp{name: "worker", runUntil: time.Millisecond, shutErr: errors.New("cannot close")}

	err := Run(context.Background(), time.Second, app)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "worker: cannot close")
}

func TestRunRejectsAnEmptySet(t *testing.T) {
	t.Parallel()

	require.Error(t, Run(context.Background(), time.Second))
}
