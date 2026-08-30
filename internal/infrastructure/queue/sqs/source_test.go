package sqs

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/murasame29/image-updater/internal/model"
)

const testQueueURL = "https://sqs.ap-northeast-1.amazonaws.com/123456789012/test-queue"

// fakeAPI hands out prepared batches and records the acknowledgements.
type fakeAPI struct {
	batches chan []types.Message

	mu      sync.Mutex
	deleted []string
}

func newFakeAPI(batches ...[]types.Message) *fakeAPI {
	api := &fakeAPI{batches: make(chan []types.Message, len(batches))}
	for _, batch := range batches {
		api.batches <- batch
	}
	return api
}

func (f *fakeAPI) ReceiveMessage(ctx context.Context, _ *awssqs.ReceiveMessageInput, _ ...func(*awssqs.Options)) (*awssqs.ReceiveMessageOutput, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case batch := <-f.batches:
		return &awssqs.ReceiveMessageOutput{Messages: batch}, nil
	}
}

func (f *fakeAPI) ChangeMessageVisibility(_ context.Context, _ *awssqs.ChangeMessageVisibilityInput, _ ...func(*awssqs.Options)) (*awssqs.ChangeMessageVisibilityOutput, error) {
	return &awssqs.ChangeMessageVisibilityOutput{}, nil
}

func (f *fakeAPI) DeleteMessage(_ context.Context, in *awssqs.DeleteMessageInput, _ ...func(*awssqs.Options)) (*awssqs.DeleteMessageOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, aws.ToString(in.ReceiptHandle))
	return &awssqs.DeleteMessageOutput{}, nil
}

func (f *fakeAPI) acknowledged() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.deleted...)
}

// jsonDecoder reads the tiny payload the tests use: the body is the image tag,
// and the literal "ignore" or "broken" selects the failure to exercise.
type jsonDecoder struct{}

func (jsonDecoder) Decode(payload []byte) (model.ImagePushEvent, error) {
	switch string(payload) {
	case "ignore":
		return model.ImagePushEvent{}, model.ErrEventIgnored
	case "broken":
		return model.ImagePushEvent{}, errors.New("unreadable payload")
	}

	return model.ImagePushEvent{
		Kind:       model.RegistryECR,
		Host:       "123456789012.dkr.ecr.ap-northeast-1.amazonaws.com",
		Repository: "apps/app",
		Tag:        string(payload),
	}, nil
}

func message(id, body string) types.Message {
	return types.Message{
		MessageId:     aws.String(id),
		ReceiptHandle: aws.String("receipt-" + id),
		Body:          aws.String(body),
	}
}

// runSource starts the source, waits for want acknowledgements, then stops it.
func runSource(t *testing.T, api *fakeAPI, handler model.EventHandler, want int) []string {
	t.Helper()

	source, err := NewSource(api, jsonDecoder{}, Config{QueueURL: testQueueURL, Concurrency: 1})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- source.Run(ctx, handler) }()

	require.Eventually(t, func() bool {
		return len(api.acknowledged()) >= want
	}, 5*time.Second, 5*time.Millisecond, "expected %d acknowledgements, got %v", want, api.acknowledged())

	cancel()

	select {
	case err := <-done:
		require.NoError(t, err, "a cancelled context is a clean shutdown")
	case <-time.After(5 * time.Second):
		t.Fatal("the source did not stop after its context was cancelled")
	}

	return api.acknowledged()
}

func TestSource_RunAcknowledgesHandledMessages(t *testing.T) {
	t.Parallel()

	api := newFakeAPI([]types.Message{message("1", "abc1234")})

	var handled []string
	handler := model.EventHandlerFunc(func(_ context.Context, event model.ImagePushEvent) error {
		handled = append(handled, event.Tag)
		return nil
	})

	assert.Equal(t, []string{"receipt-1"}, runSource(t, api, handler, 1))
	assert.Equal(t, []string{"abc1234"}, handled)
}

func TestSource_RunAcknowledgesUndecodableMessages(t *testing.T) {
	t.Parallel()

	api := newFakeAPI([]types.Message{message("1", "ignore"), message("2", "broken")})

	handler := model.EventHandlerFunc(func(context.Context, model.ImagePushEvent) error {
		t.Error("an undecodable payload must not reach the handler")
		return nil
	})

	assert.ElementsMatch(t, []string{"receipt-1", "receipt-2"}, runSource(t, api, handler, 2))
}

func TestSource_RunKeepsRetryableFailuresOnTheQueue(t *testing.T) {
	t.Parallel()

	// Concurrency is 1, so the second message is only reached once the first is
	// done. Waiting for its acknowledgement makes the assertion deterministic.
	api := newFakeAPI([]types.Message{message("1", "retry-me"), message("2", "abc1234")})

	handler := model.EventHandlerFunc(func(_ context.Context, event model.ImagePushEvent) error {
		if event.Tag == "retry-me" {
			return model.Retryable(errors.New("github is unreachable"))
		}
		return nil
	})

	assert.Equal(t, []string{"receipt-2"}, runSource(t, api, handler, 1))
}

func TestSource_RunAcknowledgesTerminalFailures(t *testing.T) {
	t.Parallel()

	api := newFakeAPI([]types.Message{message("1", "abc1234")})

	handler := model.EventHandlerFunc(func(context.Context, model.ImagePushEvent) error {
		return model.ErrNoDifference
	})

	assert.Equal(t, []string{"receipt-1"}, runSource(t, api, handler, 1))
}

func TestNewSource(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		api     API
		decoder model.EventDecoder
		cfg     Config
		wantErr bool
	}{
		{name: "有効な設定", api: newFakeAPI(), decoder: jsonDecoder{}, cfg: Config{QueueURL: testQueueURL}},
		{name: "api が nil なら拒否", decoder: jsonDecoder{}, cfg: Config{QueueURL: testQueueURL}, wantErr: true},
		{name: "decoder が nil なら拒否", api: newFakeAPI(), cfg: Config{QueueURL: testQueueURL}, wantErr: true},
		{name: "queue URL が空なら拒否", api: newFakeAPI(), decoder: jsonDecoder{}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			source, err := NewSource(tt.api, tt.decoder, tt.cfg)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "sqs", source.Name())

			// The defaults have to be filled in, otherwise SQS rejects the call.
			assert.Equal(t, int32(defaultMaxMessages), source.cfg.MaxMessages)
			assert.Equal(t, int32(defaultWaitTime), source.cfg.WaitTime)
			assert.Equal(t, int32(defaultVisibilityTimeout), source.cfg.VisibilityTimeout)
			assert.Equal(t, defaultConcurrency, source.cfg.Concurrency)

			require.Error(t, source.Run(context.Background(), nil), "a nil handler has to be rejected")
		})
	}
}
