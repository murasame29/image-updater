// Package sqs delivers image push events from an Amazon SQS queue.
//
// The queue is only a transport. What a payload means is the decoder's business,
// so the same source carries ECR events today and another registry's events
// tomorrow by swapping the decoder it was built with.
package sqs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"golang.org/x/sync/errgroup"

	"github.com/murasame29/image-updater/internal/model"
)

const (
	defaultMaxMessages       = 10
	defaultVisibilityTimeout = 20
	defaultWaitTime          = 20
	defaultConcurrency       = 10

	// receiveBackoff throttles the loop after a failed receive so an unreachable
	// queue does not turn into a tight loop.
	receiveBackoff = 5 * time.Second

	// deleteTimeout caps the acknowledgement of a processed message.
	deleteTimeout = 10 * time.Second

	// maxVisibilityRenewCallTimeout keeps a stalled renewal from consuming the
	// remaining lease margin before a retry can start.
	maxVisibilityRenewCallTimeout = 5 * time.Second

	// SQS measures its visibility maximum from ReceiveMessage, not from the most
	// recent renewal. Keep one second of margin for network and server time.
	maxMessageVisibilityLifetime = 12 * time.Hour
	visibilityLifetimeMargin     = time.Second
)

// API is the subset of the SQS client the source uses.
type API interface {
	ReceiveMessage(ctx context.Context, params *sqs.ReceiveMessageInput, optFns ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	ChangeMessageVisibility(ctx context.Context, params *sqs.ChangeMessageVisibilityInput, optFns ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityOutput, error)
	DeleteMessage(ctx context.Context, params *sqs.DeleteMessageInput, optFns ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error)
}

// Config tunes the long polling loop.
type Config struct {
	// QueueURL is the queue to poll.
	QueueURL string
	// MaxMessages is how many messages one receive may return.
	MaxMessages int32
	// VisibilityTimeout is how long a received message stays hidden from other
	// consumers, in seconds.
	VisibilityTimeout int32
	// WaitTime is the long polling wait, in seconds.
	WaitTime int32
	// Concurrency is how many messages of a batch are handled at once.
	Concurrency int
	// PollInterval is an extra pause between batches. Long polling already
	// blocks, so zero is a reasonable value.
	PollInterval time.Duration
}

func (c *Config) applyDefaults() {
	if c.MaxMessages <= 0 {
		c.MaxMessages = defaultMaxMessages
	}
	if c.VisibilityTimeout <= 0 {
		c.VisibilityTimeout = defaultVisibilityTimeout
	}
	if c.WaitTime <= 0 {
		c.WaitTime = defaultWaitTime
	}
	if c.Concurrency <= 0 {
		c.Concurrency = defaultConcurrency
	}
}

// Source long polls a queue and turns every message into a domain event.
type Source struct {
	api     API
	decoder model.EventDecoder
	cfg     Config
}

var _ model.EventSource = (*Source)(nil)

// NewSource builds a source that decodes the payloads of queueURL with decoder.
func NewSource(api API, decoder model.EventDecoder, cfg Config) (*Source, error) {
	if api == nil {
		return nil, errors.New("sqs: api is nil")
	}
	if decoder == nil {
		return nil, errors.New("sqs: event decoder is nil")
	}
	if cfg.QueueURL == "" {
		return nil, errors.New("sqs: queue URL is empty")
	}
	if cfg.MaxMessages < 0 || cfg.VisibilityTimeout < 0 || cfg.WaitTime < 0 || cfg.Concurrency < 0 || cfg.PollInterval < 0 {
		return nil, errors.New("sqs: numeric configuration must not be negative")
	}

	cfg.applyDefaults()

	switch {
	case cfg.MaxMessages > 10:
		return nil, fmt.Errorf("sqs: max messages must be between 1 and 10, got %d", cfg.MaxMessages)
	case cfg.VisibilityTimeout > 43200:
		return nil, fmt.Errorf("sqs: visibility timeout must be at most 43200 seconds, got %d", cfg.VisibilityTimeout)
	case cfg.WaitTime > 20:
		return nil, fmt.Errorf("sqs: wait time must be at most 20 seconds, got %d", cfg.WaitTime)
	case cfg.Concurrency > 10:
		return nil, fmt.Errorf("sqs: concurrency must be between 1 and 10, got %d", cfg.Concurrency)
	}

	return &Source{api: api, decoder: decoder, cfg: cfg}, nil
}

// Name identifies the source in logs.
func (s *Source) Name() string { return "sqs" }

// Run polls the queue until ctx is cancelled.
//
// A cancelled context is a clean shutdown, so it is not reported as an error.
func (s *Source) Run(ctx context.Context, handler model.EventHandler) error {
	if handler == nil {
		return errors.New("sqs: event handler is nil")
	}

	slog.InfoContext(ctx, "polling for image push events",
		slog.String("event_source", s.Name()),
		slog.String("messaging.destination.name", s.cfg.QueueURL),
	)

	for ctx.Err() == nil {
		messages, err := s.receive(ctx)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			slog.ErrorContext(ctx, "failed to receive messages",
				slog.String("error.type", fmt.Sprintf("%T", err)),
				slog.String("error.message", err.Error()),
			)
			if !sleep(ctx, receiveBackoff) {
				break
			}
			continue
		}

		if len(messages) > 0 {
			slog.DebugContext(ctx, "received messages", slog.Int("messaging.batch.message_count", len(messages)))
			s.dispatch(ctx, handler, messages)
		}

		if !sleep(ctx, s.cfg.PollInterval) {
			break
		}
	}

	slog.InfoContext(ctx, "stopped polling", slog.String("event_source", s.Name()))
	return nil
}

func (s *Source) receive(ctx context.Context) ([]types.Message, error) {
	// A visibility lease starts when SQS returns the batch, not when a handler
	// goroutine eventually starts. Never receive more work than can begin now.
	maxMessages := min(s.cfg.MaxMessages, int32(s.cfg.Concurrency))

	output, err := s.api.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            &s.cfg.QueueURL,
		MaxNumberOfMessages: maxMessages,
		VisibilityTimeout:   s.cfg.VisibilityTimeout,
		WaitTimeSeconds:     s.cfg.WaitTime,
	})
	if err != nil {
		return nil, err
	}

	return output.Messages, nil
}

// dispatch handles a batch concurrently.
//
// A message that fails must not cancel its siblings, so every failure is dealt
// with inside process and the group is only here for the concurrency limit and
// the wait.
func (s *Source) dispatch(ctx context.Context, handler model.EventHandler, messages []types.Message) {
	group := &errgroup.Group{}
	group.SetLimit(s.cfg.Concurrency)

	for _, message := range messages {
		group.Go(func() error {
			s.process(ctx, handler, message)
			return nil
		})
	}

	_ = group.Wait()
}

func (s *Source) process(ctx context.Context, handler model.EventHandler, message types.Message) {
	started := time.Now()
	messageID := aws.ToString(message.MessageId)
	receiptHandle := aws.ToString(message.ReceiptHandle)

	stopHeartbeat := s.startVisibilityHeartbeat(ctx, messageID, receiptHandle)
	defer stopHeartbeat()

	event, err := s.decoder.Decode([]byte(aws.ToString(message.Body)))
	if err != nil {
		// Neither an ignored nor an unreadable payload becomes valid later, so
		// the message is acknowledged in both cases.
		if errors.Is(err, model.ErrEventIgnored) {
			slog.DebugContext(ctx, "skipping message",
				slog.String("messaging.message.id", messageID),
				slog.String("reason", err.Error()),
			)
		} else {
			slog.ErrorContext(ctx, "failed to decode message",
				slog.String("messaging.message.id", messageID),
				slog.String("error.type", fmt.Sprintf("%T", err)),
				slog.String("error.message", err.Error()),
			)
		}
		s.acknowledge(ctx, messageID, receiptHandle)
		return
	}

	if err := handler.Handle(ctx, event); err != nil {
		if model.IsRetryable(err) {
			slog.ErrorContext(ctx, "update failed, leaving the message for redelivery",
				slog.String("messaging.message.id", messageID),
				slog.Any("event", event),
				slog.String("error.type", fmt.Sprintf("%T", err)),
				slog.String("error.message", err.Error()),
			)
			return
		}

		// A terminal outcome is expected traffic: a denied tag, a manifest that
		// already carries the new tag, an update that is already open.
		slog.WarnContext(ctx, "update not applied",
			slog.String("messaging.message.id", messageID),
			slog.Any("event", event),
			slog.String("error.message", err.Error()),
		)
	}

	s.acknowledge(ctx, messageID, receiptHandle)

	slog.DebugContext(ctx, "message processed",
		slog.String("messaging.message.id", messageID),
		slog.Float64("duration_ms", float64(time.Since(started).Microseconds())/1000),
	)
}

// startVisibilityHeartbeat keeps a received message hidden while its handler is
// active. The returned stop function owns and joins the heartbeat goroutine.
func (s *Source) startVisibilityHeartbeat(ctx context.Context, messageID, receiptHandle string) func() {
	if receiptHandle == "" {
		return func() {}
	}

	// Keep the lease alive through cancellation-independent acknowledgement.
	// The explicit stop function remains the owner of this goroutine.
	heartbeatCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.renewVisibility(heartbeatCtx, messageID, receiptHandle)
	}()

	return func() {
		cancel()
		<-done
	}
}

func (s *Source) renewVisibility(ctx context.Context, messageID, receiptHandle string) {
	configuredLease := time.Duration(s.cfg.VisibilityTimeout) * time.Second
	regularDelay := configuredLease / 2
	retryDelay := min(time.Second, regularDelay/4)
	startedAt := time.Now()
	leaseDeadline := startedAt.Add(configuredLease)

	timer := time.NewTimer(regularDelay)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}

		now := time.Now()
		remainingLifetime := maxMessageVisibilityLifetime - now.Sub(startedAt) - visibilityLifetimeMargin
		renewalLease := min(configuredLease, remainingLifetime.Truncate(time.Second))
		if renewalLease < time.Second {
			slog.WarnContext(ctx, "message visibility reached the SQS lifetime limit",
				slog.String("messaging.message.id", messageID),
			)
			return
		}

		remainingLease := time.Until(leaseDeadline)
		callTimeout := min(maxVisibilityRenewCallTimeout, remainingLease/2)
		if callTimeout <= 0 {
			callTimeout = retryDelay
		}
		callCtx, cancel := context.WithTimeout(ctx, callTimeout)
		_, err := s.api.ChangeMessageVisibility(callCtx, &sqs.ChangeMessageVisibilityInput{
			QueueUrl:          &s.cfg.QueueURL,
			ReceiptHandle:     &receiptHandle,
			VisibilityTimeout: int32(renewalLease / time.Second),
		})
		cancel()

		if ctx.Err() != nil {
			return
		}
		if err == nil {
			leaseDeadline = time.Now().Add(renewalLease)
			timer.Reset(renewalLease / 2)
			continue
		}

		slog.ErrorContext(ctx, "failed to renew message visibility",
			slog.String("messaging.message.id", messageID),
			slog.String("error.type", fmt.Sprintf("%T", err)),
			slog.String("error.message", err.Error()),
		)

		remainingLease = time.Until(leaseDeadline)
		delay := retryDelay
		if remainingLease > 0 {
			delay = min(delay, remainingLease/2)
		}
		if delay <= 0 {
			delay = retryDelay
		}
		timer.Reset(delay)
	}
}

// acknowledge deletes a message that must not come back.
//
// The parent context may already be cancelled by a shutdown while the work was
// finished successfully, so the deletion gets a context of its own to avoid
// handling the same event twice.
func (s *Source) acknowledge(ctx context.Context, messageID, receiptHandle string) {
	if receiptHandle == "" {
		return
	}

	deleteCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), deleteTimeout)
	defer cancel()

	if _, err := s.api.DeleteMessage(deleteCtx, &sqs.DeleteMessageInput{
		QueueUrl:      &s.cfg.QueueURL,
		ReceiptHandle: &receiptHandle,
	}); err != nil {
		slog.ErrorContext(ctx, "failed to delete the message",
			slog.String("messaging.message.id", messageID),
			slog.String("error.type", fmt.Sprintf("%T", err)),
			slog.String("error.message", err.Error()),
		)
	}
}

// sleep waits for d, reporting false when ctx was cancelled first.
func sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}

	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
