package model

import (
	"errors"
	"fmt"
)

var (
	// ErrEventIgnored means the payload is not a push this app cares about.
	// The delivery is acknowledged without any further work.
	ErrEventIgnored = errors.New("event ignored")

	// ErrNoMatchingRule means no rule of the rule set covers the pushed image.
	ErrNoMatchingRule = errors.New("no matching rule")

	// ErrImageTagNotAllowed means the pushed tag does not satisfy allowImageTag.
	ErrImageTagNotAllowed = errors.New("image tag not allowed")

	// ErrImageTagDenied means the pushed tag matches one of denyImageTag.
	ErrImageTagDenied = errors.New("image tag denied")

	// ErrIncompleteRule means the matched rule could not be expanded into a
	// manifest location, usually because the pushed tag has fewer dot separated
	// pieces than the tag pattern of the rule.
	ErrIncompleteRule = errors.New("incomplete rule")

	// ErrManifestNotFound means the manifest file the rule points at is missing.
	ErrManifestNotFound = errors.New("manifest not found")

	// ErrImageNotManaged means the manifests do not reference the pushed image.
	ErrImageNotManaged = errors.New("image not referenced by the manifests")

	// ErrNoDifference means the manifests already point at the pushed tag.
	ErrNoDifference = errors.New("no difference found in the manifests")

	// ErrDuplicatePullRequest means an update for the same image and tag has
	// already been pushed, so the branch exists on the remote.
	ErrDuplicatePullRequest = errors.New("duplicate pull request")

	// ErrRetryable marks a failure that has to be retried on a later delivery.
	// Wrap transient failures with Retryable; everything else is terminal and
	// the event source acknowledges the delivery.
	ErrRetryable = errors.New("retryable")
)

// Retryable marks err as worth retrying, so the event source leaves the
// delivery unacknowledged and the event comes back later.
func Retryable(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %w", ErrRetryable, err)
}

// IsRetryable reports whether err has to be retried on a later delivery.
func IsRetryable(err error) bool {
	return errors.Is(err, ErrRetryable)
}
