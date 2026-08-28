// Package updater turns an image push event into a pull request that bumps the
// image tag in the deployment manifests.
//
// The use case only talks to the ports of internal/model, so which registry the
// event came from, which git host holds the manifests and which manifest format
// is in use are all decisions taken in the container, not here.
package updater

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/murasame29/image-updater/internal/model"
)

const (
	// branchFormat names the branch an update is pushed on:
	// image_updater_{image}_{env}_{tag}
	branchFormat = "image_updater_%s_%s_%s"

	// titleFormat and commitFormat are the wording the team greps for.
	titleFormat  = "[%s][Image Updater][%s] イメージの更新"
	commitFormat = "[%s][image-committer][%s] イメージの更新"

	// invalidBranchCharacters are the characters git refuses in a ref name.
	invalidBranchCharacters = " \t~^:?*[\\\x7f"
)

// Service applies image push events to the deployment manifests.
type Service struct {
	rules     model.RuleSet
	resolver  model.MetadataResolver
	manifests model.ManifestRepository
	patcher   model.ManifestPatcher

	// annotateFromLabels turns the label driven annotation on. It is off by
	// default because it needs the build pipeline to attach the labels and read
	// access to the registry, neither of which can be assumed.
	annotateFromLabels bool
}

// Option tunes a Service.
type Option func(*Service)

// WithImageLabelAnnotation turns the label driven annotation on or off.
//
// When on, the labels baked into the pushed image are read and used to fill the
// pull request description, to pick the assignee and reviewer, and to write the
// well-known image manifest comment block. When off, the registry is never asked
// for metadata and the pull request carries only the tag change.
func WithImageLabelAnnotation(enabled bool) Option {
	return func(s *Service) { s.annotateFromLabels = enabled }
}

var _ model.EventHandler = (*Service)(nil)

// NewService wires the use case to its ports.
//
// resolver may be nil, which has the same effect as leaving the label driven
// annotation off.
func NewService(
	rules model.RuleSet,
	resolver model.MetadataResolver,
	manifests model.ManifestRepository,
	patcher model.ManifestPatcher,
	opts ...Option,
) (*Service, error) {
	switch {
	case rules.Len() == 0:
		return nil, errors.New("updater: the rule set is empty")
	case manifests == nil:
		return nil, errors.New("updater: manifest repository is nil")
	case patcher == nil:
		return nil, errors.New("updater: manifest patcher is nil")
	}

	service := &Service{rules: rules, resolver: resolver, manifests: manifests, patcher: patcher}

	for _, opt := range opts {
		opt(service)
	}

	return service, nil
}

// Handle updates the manifests that track the pushed image.
//
// Failures split in two: a transient one is wrapped with model.Retryable so the
// event source brings the event back, everything else is terminal and the event
// is done with. Deciding that here, rather than in the transport, is what keeps
// the retry policy in one place.
//
// Returns:
//
//	nil when a pull request was opened, a terminal error when the event needs no
//	action, or a retryable error when it has to be tried again.
func (s *Service) Handle(ctx context.Context, event model.ImagePushEvent) error {
	target, err := s.plan(event)
	if err != nil {
		return err
	}

	return s.apply(ctx, target)
}

// plan is everything derived from an event before a single remote call is made.
type plan struct {
	event               model.ImagePushEvent
	location            model.ManifestLocation
	env                 string
	branch              string
	writesImageManifest bool
}

// plan matches the event against the rules and works out what to change.
func (s *Service) plan(event model.ImagePushEvent) (plan, error) {
	matched, err := s.rules.Match(event)
	if err != nil {
		return plan{}, err
	}

	if err := matched.ValidateTag(); err != nil {
		return plan{}, err
	}

	location, err := matched.Location()
	if err != nil {
		return plan{}, err
	}

	branch := branchName(event, matched.Env())
	if err := validateBranch(branch); err != nil {
		return plan{}, err
	}

	return plan{
		event:    event,
		location: location,
		env:      matched.Env(),
		branch:   branch,
		// The comment block is metadata read off the image, so it needs the
		// annotation turned on. The rule can still opt out of it on its own.
		writesImageManifest: s.annotateFromLabels && matched.WritesImageManifest(),
	}, nil
}

// apply carries the plan out against the manifest repository.
func (s *Service) apply(ctx context.Context, target plan) error {
	labels := s.imageLabels(ctx, target.event)

	checkout, err := s.manifests.Checkout(ctx, target.location.RepositoryURL)
	if err != nil {
		return model.Retryable(err)
	}
	defer func() {
		if err := checkout.Close(); err != nil {
			slog.WarnContext(ctx, "failed to remove the working copy", slog.String("error.message", err.Error()))
		}
	}()

	// The branch is created before the manifests are edited: go-git refuses to
	// switch branches with a dirty working copy.
	if err := checkout.CreateBranch(ctx, target.branch); err != nil {
		return model.Retryable(err)
	}

	if err := s.patcher.Patch(ctx, filepath.Join(checkout.Dir(), target.location.Dir), target.update(labels)); err != nil {
		// A manifest that does not reference the image, or already carries the
		// tag, is a configuration fact rather than a failure. Retrying it would
		// give the same answer forever.
		return err
	}

	if err := checkout.Commit(ctx, target.commitMessage()); err != nil {
		if errors.Is(err, model.ErrNoDifference) {
			return err
		}
		return model.Retryable(err)
	}

	if err := checkout.Push(ctx, target.branch); err != nil {
		if errors.Is(err, model.ErrDuplicatePullRequest) {
			return err
		}
		return model.Retryable(err)
	}

	url, err := s.manifests.CreatePullRequest(ctx, target.pullRequest(labels))
	if err != nil {
		return model.Retryable(err)
	}

	slog.InfoContext(ctx, "opened the image update pull request",
		slog.Any("event", target.event),
		slog.String("deployment.environment.name", target.env),
		slog.String("vcs.ref.head.name", target.branch),
		slog.String("vcs.change.url", url),
	)

	return nil
}

// imageLabels reads the labels baked into the pushed image.
//
// They only decorate the pull request, so a registry that cannot be read costs
// metadata rather than the update itself.
func (s *Service) imageLabels(ctx context.Context, event model.ImagePushEvent) model.ImageLabels {
	if !s.annotateFromLabels || s.resolver == nil {
		return model.ImageLabels{}
	}

	ref := event.Reference()

	metadata, err := s.resolver.Resolve(ctx, ref)
	if err != nil {
		slog.WarnContext(ctx, "failed to read the image metadata, continuing without it",
			slog.Any("image", ref),
			slog.String("error.message", err.Error()),
		)
		return model.ImageLabels{}
	}

	return metadata.ImageLabels()
}

// update is the change to hand to the manifest patcher.
func (p plan) update(labels model.ImageLabels) model.ImageUpdate {
	image := p.event.Reference().Name()

	update := model.ImageUpdate{Image: image, NewTag: p.event.Tag}
	if p.writesImageManifest {
		manifest := model.NewImageManifest(image, labels)
		update.Manifest = &manifest
	}

	return update
}

func (p plan) commitMessage() string {
	return fmt.Sprintf(commitFormat, p.env, p.event.Repository)
}

func (p plan) pullRequest(labels model.ImageLabels) model.PullRequest {
	// The build actor is the person whose push produced the image, so they are
	// the one who should look at the update.
	var actors []string
	if labels.BuildActor != "" {
		actors = []string{labels.BuildActor}
	}

	return model.PullRequest{
		Owner:      p.location.Owner,
		Repository: p.location.Repository,
		Head:       p.branch,
		Title:      fmt.Sprintf(titleFormat, p.env, imageName(p.event.Repository)),
		Body:       FormatPullRequestBody(labels),
		Assignees:  actors,
		Reviewers:  actors,
	}
}

// imageName is the repository without its leading namespace, which is how the
// team refers to an image in branch names and pull request titles.
//
//	apps/platform/image-updater -> platform/image-updater
func imageName(repository string) string {
	segments := strings.Split(strings.Trim(repository, "/"), "/")
	if len(segments) > 1 {
		segments = segments[1:]
	}
	return strings.Join(segments, "/")
}

// branchName builds image_updater_{image}_{env}_{tag}.
func branchName(event model.ImagePushEvent, env string) string {
	return fmt.Sprintf(branchFormat,
		strings.ReplaceAll(imageName(event.Repository), "/", "_"),
		env,
		event.Tag,
	)
}

// validateBranch rejects a name git would not accept as a ref, which would
// otherwise fail on every redelivery of the same event.
func validateBranch(branch string) error {
	switch {
	case branch == "":
		return errors.New("the branch name is empty")
	case strings.Contains(branch, ".."), strings.HasSuffix(branch, "."):
		return fmt.Errorf("the branch name %q is not a valid git ref", branch)
	case strings.HasPrefix(branch, "-"), strings.HasSuffix(branch, ".lock"):
		return fmt.Errorf("the branch name %q is not a valid git ref", branch)
	case strings.ContainsAny(branch, invalidBranchCharacters):
		return fmt.Errorf("the branch name %q is not a valid git ref", branch)
	}

	return nil
}
