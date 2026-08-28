// Package ecr adapts Amazon ECR to the registry ports.
//
// Everything ECR specific lives here: the EventBridge payload schema, how a
// registry host is named and how IAM credentials become a registry token.
// Reading the image metadata itself is plain OCI distribution API, so it is
// left to the oci package and shared with every other registry.
package ecr

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/murasame29/image-updater/internal/model"
)

// Kind is the registry kind ECR events and images are tagged with.
const Kind = model.RegistryECR

const (
	actionPush    = "PUSH"
	resultSuccess = "SUCCESS"

	// hostFormat names the registry of an account in a region. Partitions other
	// than aws (GovCloud, China) use a different suffix and are not supported.
	hostFormat = "%s.dkr.ecr.%s.amazonaws.com"
)

// pushEvent is the EventBridge "ECR Image Action" payload.
type pushEvent struct {
	Account string `json:"account"`
	Region  string `json:"region"`
	Time    string `json:"time"`
	Detail  struct {
		ActionType     string `json:"action-type"`
		Result         string `json:"result"`
		RepositoryName string `json:"repository-name"`
		ImageTag       string `json:"image-tag"`
		ImageDigest    string `json:"image-digest"`
	} `json:"detail"`
}

// Decoder turns an EventBridge "ECR Image Action" payload into a domain event.
type Decoder struct{}

var _ model.EventDecoder = Decoder{}

// Decode parses an EventBridge payload.
//
// A field the payload does not carry is not treated as a rejection, so a rule
// that forwards a narrower event shape keeps working. Only an action or a result
// that is explicitly something other than a successful push is skipped.
//
// Returns:
//
//	The event, ErrEventIgnored when the payload is not a successful push, or an
//	error when it cannot be read at all.
func (Decoder) Decode(payload []byte) (model.ImagePushEvent, error) {
	var raw pushEvent
	if err := json.Unmarshal(payload, &raw); err != nil {
		return model.ImagePushEvent{}, fmt.Errorf("failed to unmarshal the ECR event: %w", err)
	}

	if raw.Detail.ActionType != "" && !strings.EqualFold(raw.Detail.ActionType, actionPush) {
		return model.ImagePushEvent{}, fmt.Errorf("%w: action-type is %q", model.ErrEventIgnored, raw.Detail.ActionType)
	}

	if raw.Detail.Result != "" && !strings.EqualFold(raw.Detail.Result, resultSuccess) {
		return model.ImagePushEvent{}, fmt.Errorf("%w: result is %q", model.ErrEventIgnored, raw.Detail.Result)
	}

	repository := strings.Trim(strings.TrimSpace(raw.Detail.RepositoryName), "/")
	tag := strings.TrimSpace(raw.Detail.ImageTag)
	if repository == "" || tag == "" {
		return model.ImagePushEvent{}, fmt.Errorf("%w: the event carries no repository name or image tag", model.ErrEventIgnored)
	}

	if raw.Account == "" || raw.Region == "" {
		return model.ImagePushEvent{}, fmt.Errorf("the ECR event carries no account or region")
	}

	// A malformed timestamp is not worth dropping the event over; the field is
	// only used for logging.
	occurredAt, _ := time.Parse(time.RFC3339, raw.Time)

	return model.ImagePushEvent{
		Kind:       Kind,
		Host:       Host(raw.Account, raw.Region),
		Repository: repository,
		Tag:        tag,
		Digest:     raw.Detail.ImageDigest,
		OccurredAt: occurredAt,
	}, nil
}

// Host names the ECR registry of an account in a region.
func Host(account, region string) string {
	return fmt.Sprintf(hostFormat, account, region)
}
