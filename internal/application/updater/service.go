// Package updater turns an image push event into a pull request that bumps the
// image tag in the deployment manifests.
//
// The use case only talks to the ports of internal/model, so which registry the
// event came from, which git host holds the manifests and which manifest format
// is in use are all decisions taken in the container, not here.
package updater

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"

	"github.com/murasame29/image-updater/internal/model"
)

const (
	// branchFormat names the branch an update is pushed on. The suffix is a
	// stable hash of the complete source and manifest location, so repositories
	// with the same human-readable tail cannot collapse onto one branch.
	branchFormat = "image_updater_%s_%s_%s_%x"

	// legacyBranchFormat is checked for open pull requests created before branch
	// identities gained a hash. It is never used for a new push.
	legacyBranchFormat = "image_updater_%s_%s_%s"

	// invalidBranchCharacters are the characters git refuses in a ref name.
	invalidBranchCharacters = " \t~^:?*[\\\x7f"
)

// Service applies image push events to the deployment manifests.
type Service struct {
	rules       model.RuleSet
	resolver    model.MetadataResolver
	manifests   model.ManifestRepository
	directories model.ManifestDirectoryResolver
	patcher     model.ManifestPatcher
	messages    *MessageRenderer

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
	directories model.ManifestDirectoryResolver,
	patcher model.ManifestPatcher,
	messages *MessageRenderer,
	opts ...Option,
) (*Service, error) {
	switch {
	case rules.Len() == 0:
		return nil, errors.New("updater: the rule set is empty")
	case manifests == nil:
		return nil, errors.New("updater: manifest repository is nil")
	case directories == nil:
		return nil, errors.New("updater: manifest directory resolver is nil")
	case patcher == nil:
		return nil, errors.New("updater: manifest patcher is nil")
	case messages == nil:
		return nil, errors.New("updater: message renderer is nil")
	}

	service := &Service{
		rules:       rules,
		resolver:    resolver,
		manifests:   manifests,
		directories: directories,
		patcher:     patcher,
		messages:    messages,
	}

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

// eventPlan contains every manifest repository affected by one image push.
type eventPlan struct {
	event        model.ImagePushEvent
	repositories []repositoryPlan
}

// repositoryPlan is the transaction boundary for one manifest repository.
// Every target is patched in one checkout and committed in one pull request.
type repositoryPlan struct {
	event    model.ImagePushEvent
	location model.ManifestLocation
	targets  []manifestTarget
	env      string
	branch   string
}

// manifestTarget is one directory selected by a matched rule.
type manifestTarget struct {
	location            model.ManifestLocation
	env                 string
	writesImageManifest bool
}

// plan matches the event against the rules and works out every change before a
// remote call is made.
func (s *Service) plan(event model.ImagePushEvent) (eventPlan, error) {
	matchedRules, err := s.rules.Matches(event)
	if err != nil {
		return eventPlan{}, err
	}

	planned := eventPlan{event: event}
	byRepository := make(map[string]int, len(matchedRules))
	var firstTagError error

	for _, matched := range matchedRules {
		if err := matched.ValidateTag(); err != nil {
			if firstTagError == nil {
				firstTagError = err
			}
			continue
		}

		location, err := matched.Location()
		if err != nil {
			return eventPlan{}, err
		}

		target := manifestTarget{
			location: location,
			env:      matched.Env(),
			// The comment block is metadata read off the image, so it needs
			// annotation enabled globally. A rule can still opt out on its own.
			writesImageManifest: s.annotateFromLabels && matched.WritesImageManifest(),
		}

		repositoryKey := manifestRepositoryKey(location)
		index, exists := byRepository[repositoryKey]
		if !exists {
			index = len(planned.repositories)
			byRepository[repositoryKey] = index
			planned.repositories = append(planned.repositories, repositoryPlan{
				event:    event,
				location: location,
			})
		}
		planned.repositories[index].targets = append(planned.repositories[index].targets, target)
	}

	if len(planned.repositories) == 0 {
		return eventPlan{}, firstTagError
	}

	// A deterministic order makes branch identities and retry behavior stable,
	// while processing repositories sequentially bounds concurrent clone memory.
	sort.Slice(planned.repositories, func(i, j int) bool {
		return manifestRepositoryKey(planned.repositories[i].location) <
			manifestRepositoryKey(planned.repositories[j].location)
	})

	for i := range planned.repositories {
		repository := &planned.repositories[i]
		repository.targets, err = normalizeManifestTargets(repository.location.RepositoryURL, repository.targets)
		if err != nil {
			return eventPlan{}, err
		}

		environments := make([]string, 0, len(repository.targets))
		seenEnvironments := make(map[string]struct{}, len(repository.targets))
		for _, target := range repository.targets {
			if _, exists := seenEnvironments[target.env]; exists {
				continue
			}
			seenEnvironments[target.env] = struct{}{}
			environments = append(environments, target.env)
		}
		sort.Strings(environments)
		repository.env = strings.Join(environments, ",")

		branchEnv := repository.env
		if len(environments) > 1 {
			branchEnv = "multi"
		}
		repository.branch = repositoryBranchName(event, branchEnv, *repository)
		if err := validateBranch(repository.branch); err != nil {
			return eventPlan{}, err
		}
	}

	return planned, nil
}

func normalizeManifestTargets(repositoryURL string, targets []manifestTarget) ([]manifestTarget, error) {
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].location.Dir == targets[j].location.Dir {
			return targets[i].env < targets[j].env
		}
		return targets[i].location.Dir < targets[j].location.Dir
	})

	unique := targets[:0]
	for _, target := range targets {
		if len(unique) > 0 && unique[len(unique)-1].location.Dir == target.location.Dir {
			previous := unique[len(unique)-1]
			if previous.env != target.env || previous.writesImageManifest != target.writesImageManifest {
				return nil, fmt.Errorf(
					"%w: conflicting rules for %s/%s",
					model.ErrIncompleteRule,
					repositoryURL,
					target.location.Dir,
				)
			}
			continue
		}
		unique = append(unique, target)
	}
	return unique, nil
}

// apply carries the plan out one manifest repository at a time. Image labels
// are resolved once per event even when several repositories are affected.
func (s *Service) apply(ctx context.Context, target eventPlan) error {
	labels := s.imageLabels(ctx, target.event)
	messages := make([]renderedMessages, len(target.repositories))

	// Render every repository before the first remote call. A configuration
	// error must not leave an event partially applied across repositories.
	for i, repository := range target.repositories {
		rendered, err := s.messages.render(messageTemplateData{
			Environment:     repository.env,
			ImageName:       imageName(repository.event.Repository),
			ImageRepository: repository.event.Repository,
			Image:           repository.event.Reference().Name(),
			ImageTag:        repository.event.Tag,
			DefaultBody:     FormatPullRequestBody(labels),
		})
		if err != nil {
			return fmt.Errorf("render messages for %s: %w", repository.location.RepositoryURL, err)
		}
		messages[i] = rendered
	}

	failures := make([]error, 0, len(target.repositories))
	for i, repository := range target.repositories {
		if err := s.applyRepository(ctx, repository, labels, messages[i]); err != nil {
			failures = append(failures, fmt.Errorf("update %s: %w", repository.location.RepositoryURL, err))
		}
	}

	return errors.Join(failures...)
}

// applyRepository patches every selected directory in one disposable checkout.
// A target failure closes the checkout without committing any target in it.
func (s *Service) applyRepository(
	ctx context.Context,
	target repositoryPlan,
	labels model.ImageLabels,
	messages renderedMessages,
) error {
	// A retryable failure in another repository redelivers the whole event. The
	// stable branch lets an already completed group avoid another clone/patch.
	if err := s.ensureNoOpenPullRequest(ctx, target); err != nil {
		return err
	}

	checkout, err := s.manifests.Checkout(ctx, target.location.RepositoryURL)
	if err != nil {
		return model.Retryable(fmt.Errorf("checkout manifest repository: %w", err))
	}
	defer func() {
		if err := checkout.Close(); err != nil {
			slog.WarnContext(ctx, "failed to remove the working copy", slog.String("error.message", err.Error()))
		}
	}()

	manifests, err := s.resolveManifestTargets(ctx, checkout.Dir(), target)
	if err != nil {
		return err
	}

	// The branch is created before the manifests are edited: go-git refuses to
	// switch branches with a dirty working copy.
	if err := checkout.CreateBranch(ctx, target.branch); err != nil {
		return model.Retryable(fmt.Errorf("create update branch: %w", err))
	}

	changed := false
	for _, manifest := range manifests {
		err := s.patcher.Patch(
			ctx,
			filepath.Join(checkout.Dir(), manifest.location.Dir),
			manifest.update(target.event, labels),
		)
		if err == nil {
			changed = true
			continue
		}
		if errors.Is(err, model.ErrNoDifference) {
			continue
		}
		// These sentinels describe stable repository/configuration state. Every
		// other failure may be transient (filesystem, parser, or I/O) and must be
		// retried rather than acknowledged and lost by the event source.
		if errors.Is(err, model.ErrManifestNotFound) ||
			errors.Is(err, model.ErrInvalidManifest) ||
			errors.Is(err, model.ErrImageNotManaged) {
			return fmt.Errorf("patch manifest %s: %w", manifest.location.Dir, err)
		}
		return model.Retryable(fmt.Errorf("patch manifest %s: %w", manifest.location.Dir, err))
	}

	if !changed {
		return fmt.Errorf("%w: %s", model.ErrNoDifference, target.location.RepositoryURL)
	}

	if err := checkout.Commit(ctx, messages.commitMessage); err != nil {
		if errors.Is(err, model.ErrNoDifference) {
			return err
		}
		return model.Retryable(fmt.Errorf("commit manifest changes: %w", err))
	}

	if err := checkout.Push(ctx, target.branch); err != nil {
		if !errors.Is(err, model.ErrDuplicatePullRequest) {
			return model.Retryable(fmt.Errorf("push update branch: %w", err))
		}

		// The branch is on the remote, but that does not mean the pull request was
		// ever opened. A failure between the push and the create leaves the branch
		// behind, and every later attempt pushes a commit with a different hash, so
		// treating the rejected push as "already done" would drop the update for
		// good. Ask before giving up.
		if err := s.ensureNoOpenPullRequest(ctx, target); err != nil {
			return err
		}

		slog.WarnContext(ctx, "the branch was pushed by an earlier attempt but no pull request exists, opening it now",
			slog.Any("event", target.event),
			slog.String("vcs.ref.head.name", target.branch),
		)
	}

	url, err := s.manifests.CreatePullRequest(ctx, target.pullRequest(labels, messages))
	if err != nil {
		wrapped := fmt.Errorf("create pull request: %w", err)
		if errors.Is(err, model.ErrInvalidPullRequest) {
			return wrapped
		}
		return model.Retryable(wrapped)
	}

	slog.InfoContext(ctx, "opened the image update pull request",
		slog.Any("event", target.event),
		slog.String("deployment.environment.name", target.env),
		slog.String("vcs.ref.head.name", target.branch),
		slog.String("vcs.change.url", url),
	)

	return nil
}

func (s *Service) resolveManifestTargets(
	ctx context.Context,
	checkoutRoot string,
	target repositoryPlan,
) ([]manifestTarget, error) {
	resolved := make([]manifestTarget, 0, len(target.targets))
	for _, source := range target.targets {
		directories, err := s.directories.Resolve(ctx, checkoutRoot, source.location.Dir)
		if err != nil {
			wrapped := fmt.Errorf("resolve manifest directories for %s: %w", source.location.Dir, err)
			if errors.Is(err, model.ErrManifestNotFound) ||
				errors.Is(err, model.ErrInvalidManifest) ||
				errors.Is(err, model.ErrIncompleteRule) {
				return nil, wrapped
			}
			return nil, model.Retryable(wrapped)
		}
		if len(directories) == 0 {
			return nil, fmt.Errorf(
				"%w: no manifest directory matches %q",
				model.ErrManifestNotFound,
				source.location.Dir,
			)
		}
		for _, directory := range directories {
			manifest := source
			manifest.location.Dir = directory
			resolved = append(resolved, manifest)
		}
	}

	normalized, err := normalizeManifestTargets(target.location.RepositoryURL, resolved)
	if err != nil {
		return nil, err
	}
	return normalized, nil
}

// ensureNoOpenPullRequest checks whether the stable branch already has a pull
// request. It is used before checkout to make redelivery cheap and after a
// rejected push to distinguish a completed update from an orphan branch.
//
// Returns:
//
//	nil when no pull request exists, ErrDuplicatePullRequest when the update is
//	already open, or a retryable error when the lookup could not be made.
func (s *Service) ensureNoOpenPullRequest(ctx context.Context, target repositoryPlan) error {
	branches := []string{target.branch}
	if legacy := legacyRepositoryBranchName(target.event, target.env, target); legacy != "" && legacy != target.branch {
		branches = append(branches, legacy)
	}

	for _, branch := range branches {
		url, err := s.manifests.FindOpenPullRequest(
			ctx,
			target.location.Owner,
			target.location.Repository,
			branch,
		)
		if err != nil {
			return model.Retryable(fmt.Errorf("find open pull request for %s: %w", branch, err))
		}
		if url != "" {
			return fmt.Errorf("%w: %s is already open at %s", model.ErrDuplicatePullRequest, branch, url)
		}
	}

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
func (t manifestTarget) update(event model.ImagePushEvent, labels model.ImageLabels) model.ImageUpdate {
	image := event.Reference().Name()

	update := model.ImageUpdate{Image: image, NewTag: event.Tag}
	if t.writesImageManifest {
		manifest := model.NewImageManifest(image, labels)
		update.Manifest = &manifest
	}

	return update
}

func (p repositoryPlan) pullRequest(labels model.ImageLabels, messages renderedMessages) model.PullRequest {
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
		Title:      messages.pullRequestTitle,
		Body:       messages.pullRequestBody,
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

// manifestRepositoryKey canonicalizes GitHub's case-insensitive owner and
// repository identity for grouping and multi-target branch hashing.
func manifestRepositoryKey(location model.ManifestLocation) string {
	return strings.ToLower(location.Owner) + "\x00" + strings.ToLower(location.Repository)
}

// repositoryBranchName builds one stable branch identity for every target in a
// manifest repository. A single target keeps its historical identity input but
// also receives the hash suffix used by every newly created branch.
func repositoryBranchName(event model.ImagePushEvent, env string, target repositoryPlan) string {
	if len(target.targets) == 1 {
		return branchName(event, env, target.targets[0].location)
	}

	identity := make([]string, 0, 4+len(target.targets)*2)
	identity = append(identity, event.Host, event.Repository, manifestRepositoryKey(target.location), event.Tag)
	for _, manifest := range target.targets {
		identity = append(identity, manifest.location.Dir, manifest.env)
	}
	digest := sha256.Sum256([]byte(strings.Join(identity, "\x00")))

	return fmt.Sprintf(branchFormat,
		strings.ReplaceAll(imageName(event.Repository), "/", "_"),
		env,
		event.Tag,
		digest[:8],
	)
}

// legacyRepositoryBranchName returns the pre-hash branch name for a single
// target so an open pull request created by the previous release still blocks
// a duplicate update after deployment. New branches always use branchFormat.
func legacyRepositoryBranchName(event model.ImagePushEvent, env string, target repositoryPlan) string {
	if len(target.targets) != 1 {
		return ""
	}
	return fmt.Sprintf(
		legacyBranchFormat,
		strings.ReplaceAll(imageName(event.Repository), "/", "_"),
		env,
		event.Tag,
	)
}

// branchName builds a readable branch name with a stable identity suffix.
func branchName(event model.ImagePushEvent, env string, location model.ManifestLocation) string {
	identity := strings.Join([]string{
		event.Host,
		event.Repository,
		location.RepositoryURL,
		location.Dir,
		env,
		event.Tag,
	}, "\x00")
	digest := sha256.Sum256([]byte(identity))

	return fmt.Sprintf(branchFormat,
		strings.ReplaceAll(imageName(event.Repository), "/", "_"),
		env,
		event.Tag,
		digest[:8],
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
