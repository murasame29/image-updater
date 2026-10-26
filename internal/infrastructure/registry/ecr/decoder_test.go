package ecr

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/murasame29/image-updater/internal/model"
)

const pushPayload = `{
  "version": "0",
  "id": "6b1b5b0e-0000-0000-0000-000000000000",
  "detail-type": "ECR Image Action",
  "source": "aws.ecr",
  "account": "123456789012",
  "time": "2026-08-27T01:02:03Z",
  "region": "ap-northeast-1",
  "detail": {
    "action-type": "PUSH",
    "result": "SUCCESS",
    "repository-name": "apps/samples/app",
    "image-digest": "sha256:abc",
    "image-tag": "alice.790bf3ee04b441a96fb3d1860aea91fa09b72747"
  }
}`

func TestDecoder_Decode(t *testing.T) {
	t.Parallel()

	event, err := Decoder{}.Decode([]byte(pushPayload))
	require.NoError(t, err)

	assert.Equal(t, model.RegistryECR, event.Kind)
	assert.Equal(t, "123456789012.dkr.ecr.ap-northeast-1.amazonaws.com", event.Host)
	assert.Equal(t, "apps/samples/app", event.Repository)
	assert.Equal(t, "alice.790bf3ee04b441a96fb3d1860aea91fa09b72747", event.Tag)
	assert.Equal(t, "sha256:abc", event.Digest)
	assert.Equal(t, time.Date(2026, 8, 27, 1, 2, 3, 0, time.UTC), event.OccurredAt.UTC())

	assert.Equal(t,
		"123456789012.dkr.ecr.ap-northeast-1.amazonaws.com/apps/samples/app",
		event.Reference().Name(),
	)
}

// replicationPayload is the event ECR emits in the destination region once a
// cross-region replication has landed. The shape follows the AWS sample.
const replicationPayload = `{
  "version": "0",
  "id": "c8b133b1-6029-ee73-e2a1-4f466b8ba999",
  "detail-type": "ECR Replication Action",
  "source": "aws.ecr",
  "account": "123456789012",
  "time": "2026-08-27T01:02:30Z",
  "region": "us-west-2",
  "resources": [
    "arn:aws:ecr:us-west-2:123456789012:repository/apps/samples/app"
  ],
  "detail": {
    "result": "SUCCESS",
    "repository-name": "apps/samples/app",
    "image-digest": "sha256:abc",
    "source-account": "123456789012",
    "action-type": "REPLICATE",
    "source-region": "ap-northeast-1",
    "image-tag": "alice.790bf3ee04b441a96fb3d1860aea91fa09b72747"
  }
}`

func TestDecoder_DecodeReplication(t *testing.T) {
	t.Parallel()

	event, err := Decoder{}.Decode([]byte(replicationPayload))
	require.NoError(t, err)

	// The host is the destination registry, not the source the image was
	// pushed to, so rules written for the destination region match.
	assert.Equal(t, "123456789012.dkr.ecr.us-west-2.amazonaws.com", event.Host)
	assert.Equal(t, "apps/samples/app", event.Repository)
	assert.Equal(t, "alice.790bf3ee04b441a96fb3d1860aea91fa09b72747", event.Tag)
	assert.Equal(t, "sha256:abc", event.Digest)
	assert.Equal(t, time.Date(2026, 8, 27, 1, 2, 30, 0, time.UTC), event.OccurredAt.UTC())
}

func TestDecoder_DecodeRejections(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		payload string
		wantErr error
		errText string
	}{
		{
			name:    "DELETE は対象外",
			payload: `{"account":"1","region":"r","detail":{"action-type":"DELETE","repository-name":"a/b","image-tag":"t"}}`,
			wantErr: model.ErrEventIgnored,
		},
		{
			name:    "失敗した replication は対象外",
			payload: `{"account":"1","region":"r","detail":{"action-type":"REPLICATE","result":"FAILURE","repository-name":"a/b","image-tag":"t"}}`,
			wantErr: model.ErrEventIgnored,
		},
		{
			name:    "失敗した push は対象外",
			payload: `{"account":"1","region":"r","detail":{"action-type":"PUSH","result":"FAILURE","repository-name":"a/b","image-tag":"t"}}`,
			wantErr: model.ErrEventIgnored,
		},
		{
			name:    "タグのない push は対象外",
			payload: `{"account":"1","region":"r","detail":{"action-type":"PUSH","repository-name":"a/b"}}`,
			wantErr: model.ErrEventIgnored,
		},
		{
			name:    "リポジトリ名のない push は対象外",
			payload: `{"account":"1","region":"r","detail":{"action-type":"PUSH","image-tag":"t"}}`,
			wantErr: model.ErrEventIgnored,
		},
		{
			name:    "account がなければエラー",
			payload: `{"region":"r","detail":{"action-type":"PUSH","repository-name":"a/b","image-tag":"t"}}`,
			errText: "no account or region",
		},
		{
			name:    "JSON として壊れていればエラー",
			payload: `{`,
			errText: "failed to unmarshal",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := Decoder{}.Decode([]byte(tt.payload))
			require.Error(t, err)

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			assert.Contains(t, err.Error(), tt.errText)
		})
	}
}

func TestDecoder_DecodeToleratesAMissingActionType(t *testing.T) {
	t.Parallel()

	// A rule that forwards a narrower event shape must keep working.
	event, err := Decoder{}.Decode([]byte(
		`{"account":"123456789012","region":"ap-northeast-1","detail":{"repository-name":"apps/app","image-tag":"abc1234"}}`))
	require.NoError(t, err)

	assert.Equal(t, "apps/app", event.Repository)
	assert.Equal(t, "abc1234", event.Tag)
	assert.True(t, event.OccurredAt.IsZero(), "a missing timestamp is not a rejection")
}

func TestDecoder_DecodeTrimsTheRepositoryName(t *testing.T) {
	t.Parallel()

	event, err := Decoder{}.Decode([]byte(
		`{"account":"123456789012","region":"ap-northeast-1","detail":{"action-type":"PUSH","repository-name":"/apps/app/","image-tag":"abc1234"}}`))
	require.NoError(t, err)

	assert.Equal(t, "apps/app", event.Repository)
}

func TestHost(t *testing.T) {
	t.Parallel()

	assert.Equal(t,
		"123456789012.dkr.ecr.ap-northeast-1.amazonaws.com",
		Host("123456789012", "ap-northeast-1"),
	)
}
