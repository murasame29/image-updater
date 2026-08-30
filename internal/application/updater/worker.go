package updater

import (
	"context"
	"errors"

	"github.com/murasame29/image-updater/internal/model"
)

// Worker runs an event source against a handler for the life of the process.
//
// It is what turns the use case into something the process lifecycle can start
// and stop. The signature matches pkg/lifecycle.Application without importing
// it, so the application layer keeps depending on internal/model alone.
type Worker struct {
	source  model.EventSource
	handler model.EventHandler
}

// NewWorker binds source to handler.
func NewWorker(source model.EventSource, handler model.EventHandler) (*Worker, error) {
	if source == nil {
		return nil, errors.New("updater: event source is nil")
	}
	if handler == nil {
		return nil, errors.New("updater: event handler is nil")
	}

	return &Worker{source: source, handler: handler}, nil
}

// Name identifies the worker in logs.
func (w *Worker) Name() string { return "image-updater/" + w.source.Name() }

// Run consumes events until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) error {
	return w.source.Run(ctx, w.handler)
}

// Shutdown has nothing to release: Run returns once its context is cancelled and
// the event source drains the batch it is holding before it does.
func (w *Worker) Shutdown(context.Context) error { return nil }
