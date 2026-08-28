// Package github implements the manifest repository ports on top of GitHub.
//
// The go-git and go-github types never leave this package: the application layer
// only ever sees model.ManifestRepository and model.Checkout, which is what makes
// another git host a matter of adding a sibling package.
package github

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/bradleyfalzon/ghinstallation/v2"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	gogithub "github.com/google/go-github/v88/github"

	"github.com/murasame29/image-updater/internal/model"
)

const (
	// defaultBaseBranch is the branch pull requests target when the caller does
	// not name one.
	defaultBaseBranch = "main"

	// apiTimeout caps a single GitHub API call.
	apiTimeout = 30 * time.Second

	// checkoutPrefix prefixes the working copy directories, so a leftover
	// directory is recognisable.
	checkoutPrefix = "image-updater-"

	// noreplyEmailFormat is the commit author address used when none is
	// configured. An empty address makes for an invalid commit.
	noreplyEmailFormat = "%s@users.noreply.github.com"
)

// Config holds what the adapter needs to act as a GitHub App installation.
type Config struct {
	// ApplicationID and InstallationID identify the GitHub App installation.
	ApplicationID  int64
	InstallationID int64
	// Username is the git user the App pushes as.
	Username string
	// PrivateKeyPath is the PEM private key of the App.
	PrivateKeyPath string
	// AuthorName and AuthorEmail sign the commits. Both fall back to Username.
	AuthorName  string
	AuthorEmail string
	// BaseBranch is the branch pull requests target.
	BaseBranch string
	// WorkDir is where working copies are created. Empty means os.TempDir().
	WorkDir string
}

// Repository is the GitHub implementation of model.ManifestRepository.
type Repository struct {
	client    *gogithub.Client
	transport *ghinstallation.Transport
	cfg       Config
}

var _ model.ManifestRepository = (*Repository)(nil)

// NewRepository builds an adapter authenticating as a GitHub App installation.
func NewRepository(cfg Config) (*Repository, error) {
	switch {
	case cfg.ApplicationID == 0:
		return nil, errors.New("github: application ID is not set")
	case cfg.InstallationID == 0:
		return nil, errors.New("github: installation ID is not set")
	case cfg.Username == "":
		return nil, errors.New("github: username is not set")
	case cfg.PrivateKeyPath == "":
		return nil, errors.New("github: private key path is not set")
	}

	if cfg.BaseBranch == "" {
		cfg.BaseBranch = defaultBaseBranch
	}
	if cfg.AuthorName == "" {
		cfg.AuthorName = cfg.Username
	}
	if cfg.AuthorEmail == "" {
		cfg.AuthorEmail = fmt.Sprintf(noreplyEmailFormat, cfg.Username)
	}

	transport, err := ghinstallation.NewKeyFromFile(http.DefaultTransport, cfg.ApplicationID, cfg.InstallationID, cfg.PrivateKeyPath)
	if err != nil {
		return nil, fmt.Errorf("github: failed to read the App private key %s: %w", cfg.PrivateKeyPath, err)
	}

	client, err := gogithub.NewClient(
		gogithub.WithHTTPClient(&http.Client{Transport: transport, Timeout: apiTimeout}),
	)
	if err != nil {
		return nil, fmt.Errorf("github: failed to build the API client: %w", err)
	}

	return &Repository{client: client, transport: transport, cfg: cfg}, nil
}

// Checkout clones repositoryURL into a fresh working directory.
//
// Returns:
//
//	The working copy, which the caller closes, or an error when the clone fails.
func (r *Repository) Checkout(ctx context.Context, repositoryURL string) (model.Checkout, error) {
	// An installation token only lives for an hour while this process runs for
	// days, so it is fetched per checkout. The transport refreshes it as needed.
	token, err := r.transport.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get a GitHub App installation token: %w", err)
	}

	dir, err := os.MkdirTemp(r.cfg.WorkDir, checkoutPrefix)
	if err != nil {
		return nil, fmt.Errorf("failed to create a working directory: %w", err)
	}

	auth := &githttp.BasicAuth{Username: r.cfg.Username, Password: token}

	repo, err := gogit.PlainCloneContext(ctx, dir, false, &gogit.CloneOptions{
		URL:  repositoryURL,
		Auth: auth,
	})
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("failed to clone %s: %w", repositoryURL, err)
	}

	slog.DebugContext(ctx, "cloned the manifest repository",
		slog.String("vcs.repository.url.full", repositoryURL),
		slog.String("checkout.dir", dir),
	)

	return &checkout{
		dir:  dir,
		repo: repo,
		auth: auth,
		signature: func() object.Signature {
			return object.Signature{
				Name:  r.cfg.AuthorName,
				Email: r.cfg.AuthorEmail,
				When:  time.Now(),
			}
		},
	}, nil
}

// CreatePullRequest opens a pull request for an already pushed branch.
//
// Assignees and reviewers are best effort: the pull request is already open by
// then, so failing to set them must not fail the update.
//
// Returns:
//
//	The URL of the pull request, or an error when it cannot be opened.
func (r *Repository) CreatePullRequest(ctx context.Context, pr model.PullRequest) (string, error) {
	base := pr.Base
	if base == "" {
		base = r.cfg.BaseBranch
	}

	created, _, err := r.client.PullRequests.Create(ctx, pr.Owner, pr.Repository, &gogithub.NewPullRequest{
		Title: gogithub.Ptr(pr.Title),
		Head:  gogithub.Ptr(pr.Head),
		Base:  gogithub.Ptr(base),
		Body:  gogithub.Ptr(pr.Body),
	})
	if err != nil {
		return "", fmt.Errorf("failed to create the pull request on %s/%s: %w", pr.Owner, pr.Repository, err)
	}

	number := created.GetNumber()

	if assignees := nonEmpty(pr.Assignees); len(assignees) > 0 {
		if _, _, err := r.client.Issues.AddAssignees(ctx, pr.Owner, pr.Repository, number, assignees); err != nil {
			slog.WarnContext(ctx, "failed to assign the pull request",
				slog.Any("assignees", assignees),
				slog.String("error.message", err.Error()),
			)
		}
	}

	if reviewers := nonEmpty(pr.Reviewers); len(reviewers) > 0 {
		request := gogithub.ReviewersRequest{Reviewers: reviewers}
		if _, _, err := r.client.PullRequests.RequestReviewers(ctx, pr.Owner, pr.Repository, number, request); err != nil {
			slog.WarnContext(ctx, "failed to request reviewers",
				slog.Any("reviewers", reviewers),
				slog.String("error.message", err.Error()),
			)
		}
	}

	return created.GetHTMLURL(), nil
}

// nonEmpty drops the blank entries of a list of GitHub handles.
func nonEmpty(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" {
			result = append(result, value)
		}
	}
	return result
}
