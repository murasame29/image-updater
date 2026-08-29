package model

import "context"

// EventSource delivers image push events until its context is cancelled.
//
// An implementation owns one transport: SQS long polling today, a webhook
// server or a Pub/Sub subscription later. It acknowledges a delivery once the
// handler returns, unless the handler error satisfies IsRetryable, in which
// case the delivery is left for a later attempt.
type EventSource interface {
	// Name identifies the source in logs.
	Name() string
	// Run delivers events to handler until ctx is cancelled.
	Run(ctx context.Context, handler EventHandler) error
}

// EventHandler reacts to a single image push event.
type EventHandler interface {
	Handle(ctx context.Context, event ImagePushEvent) error
}

// EventHandlerFunc adapts a plain function to EventHandler.
type EventHandlerFunc func(ctx context.Context, event ImagePushEvent) error

// Handle calls f.
func (f EventHandlerFunc) Handle(ctx context.Context, event ImagePushEvent) error {
	return f(ctx, event)
}

// EventDecoder turns the raw payload carried by a transport into a domain event.
//
// It is the only place that knows a provider payload schema, which is what
// keeps a transport reusable: the same SQS source can carry ECR events today
// and Harbor events tomorrow by swapping the decoder.
type EventDecoder interface {
	// Decode parses payload. It reports ErrEventIgnored when the payload is
	// well formed but not a push this app has to act on.
	Decode(payload []byte) (ImagePushEvent, error)
}

// MetadataResolver reads the metadata baked into an image that was pushed.
//
// One implementation per RegistryKind. Most of the work is the OCI distribution
// API, which every registry in scope speaks, so implementations differ mainly
// in how they authenticate.
type MetadataResolver interface {
	Resolve(ctx context.Context, ref ImageReference) (ImageMetadata, error)
}

// ManifestRepository hosts the deployment manifests this app updates.
type ManifestRepository interface {
	// Checkout creates a working copy of repositoryURL. The caller closes it.
	Checkout(ctx context.Context, repositoryURL string) (Checkout, error)
	// CreatePullRequest opens a pull request for an already pushed branch and
	// returns its URL.
	CreatePullRequest(ctx context.Context, pr PullRequest) (string, error)
	// FindOpenPullRequest looks for an open pull request whose source branch is
	// head. It reports an empty URL when there is none, which is how the caller
	// tells "the branch is already there" from "the update is already open".
	FindOpenPullRequest(ctx context.Context, owner, repository, head string) (string, error)
}

// Checkout is a working copy of a manifest repository. It hides the git
// implementation so the application layer never touches a git library type.
type Checkout interface {
	// Dir is the absolute path of the working copy root.
	Dir() string
	// CreateBranch creates branch and switches the working copy onto it.
	CreateBranch(ctx context.Context, branch string) error
	// Commit stages the working copy and records message.
	Commit(ctx context.Context, message string) error
	// Push publishes branch to the remote. It reports ErrDuplicatePullRequest
	// when the branch is already there.
	Push(ctx context.Context, branch string) error
	// Close removes the working copy.
	Close() error
}

// ManifestDirectoryResolver expands a repository-relative manifest directory
// pattern against one checked-out working copy.
type ManifestDirectoryResolver interface {
	// Resolve returns deterministic repository-relative directories. A literal
	// pattern resolves to itself; a recursive pattern reports
	// ErrManifestNotFound when no manifest directory matches.
	Resolve(ctx context.Context, checkoutRoot, pattern string) ([]string, error)
}

// ManifestPatcher applies an image tag update to the manifests of a directory.
//
// The kustomize implementation is the only one today; a Helm values.yaml
// implementation would slot in behind the same port.
type ManifestPatcher interface {
	// Patch rewrites the manifests in dir in place. It reports
	// ErrImageNotManaged when no manifest references the image and
	// ErrNoDifference when the manifests already carry the new tag.
	Patch(ctx context.Context, dir string, update ImageUpdate) error
}
