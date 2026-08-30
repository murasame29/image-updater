package github

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/murasame29/image-updater/internal/model"
)

// checkout is a go-git working copy behind model.Checkout.
type checkout struct {
	dir       string
	repo      *gogit.Repository
	auth      transport.AuthMethod
	signature func() object.Signature
}

var _ model.Checkout = (*checkout)(nil)

// Dir is the root of the working copy.
func (c *checkout) Dir() string { return c.dir }

// CreateBranch creates branch and switches the working copy onto it.
func (c *checkout) CreateBranch(_ context.Context, branch string) error {
	worktree, err := c.repo.Worktree()
	if err != nil {
		return fmt.Errorf("failed to open the worktree: %w", err)
	}

	options := &gogit.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName(branch),
		Create: true,
	}
	if err := worktree.Checkout(options); err == nil {
		return nil
	}

	// The working copy is a fresh clone, so the branch is not supposed to exist
	// yet. Switching onto an existing one is still the right recovery.
	options.Create = false
	if err := worktree.Checkout(options); err != nil {
		return fmt.Errorf("failed to check out the branch %s: %w", branch, err)
	}

	return nil
}

// Commit stages the working copy and records message.
//
// Returns:
//
//	nil on success, ErrNoDifference when there is nothing staged, or an error
//	when git refuses the commit.
func (c *checkout) Commit(ctx context.Context, message string) error {
	worktree, err := c.repo.Worktree()
	if err != nil {
		return fmt.Errorf("failed to open the worktree: %w", err)
	}

	if _, err := worktree.Add("."); err != nil {
		return fmt.Errorf("failed to stage the working copy: %w", err)
	}

	status, err := worktree.Status()
	if err != nil {
		return fmt.Errorf("failed to read the worktree status: %w", err)
	}

	if status.IsClean() {
		return fmt.Errorf("%w: nothing to commit", model.ErrNoDifference)
	}

	hash, err := worktree.Commit(message, &gogit.CommitOptions{Author: pointerTo(c.signature())})
	if err != nil {
		return fmt.Errorf("failed to commit: %w", err)
	}

	slog.DebugContext(ctx, "recorded the commit", slog.String("vcs.revision", hash.String()))
	return nil
}

// Push publishes only the branch of this update, so concurrent updates of other
// images never contend on the default branch.
//
// Returns:
//
//	nil on success, ErrDuplicatePullRequest when the branch is already on the
//	remote, or an error for anything else.
func (c *checkout) Push(ctx context.Context, branch string) error {
	refSpec := config.RefSpec(fmt.Sprintf("refs/heads/%s:refs/heads/%s", branch, branch))

	err := c.repo.PushContext(ctx, &gogit.PushOptions{
		RefSpecs: []config.RefSpec{refSpec},
		Auth:     c.auth,
	})

	switch {
	case err == nil:
		return nil
	case errors.Is(err, gogit.NoErrAlreadyUpToDate):
		return fmt.Errorf("%w: %s is already on the remote", model.ErrDuplicatePullRequest, branch)
	case isNonFastForward(err):
		return fmt.Errorf("%w: %s already exists on the remote: %w", model.ErrDuplicatePullRequest, branch, err)
	default:
		return fmt.Errorf("failed to push %s: %w", branch, err)
	}
}

// Close removes the working copy.
func (c *checkout) Close() error {
	return os.RemoveAll(c.dir)
}

// isNonFastForward reports whether err is the non fast forward rejection of the
// remote. go-git formats that error without wrapping its sentinel, so the
// message has to be inspected as well.
func isNonFastForward(err error) bool {
	return errors.Is(err, gogit.ErrNonFastForwardUpdate) ||
		strings.Contains(err.Error(), gogit.ErrNonFastForwardUpdate.Error())
}

func pointerTo[T any](value T) *T { return &value }
