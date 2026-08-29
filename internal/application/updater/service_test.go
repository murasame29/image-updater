package updater

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/murasame29/image-updater/internal/model"
)

const (
	testRegistry = "123456789012.dkr.ecr.ap-northeast-1.amazonaws.com"
	testTag      = "alice.790bf3ee04b441a96fb3d1860aea91fa09b72747"
)

// fakeCheckout is an in-memory working copy.
type fakeCheckout struct {
	dir string

	branches []string
	commits  []string
	pushes   []string
	closed   bool

	branchErr error
	commitErr error
	pushErr   error
}

func (c *fakeCheckout) Dir() string { return c.dir }

func (c *fakeCheckout) CreateBranch(_ context.Context, branch string) error {
	c.branches = append(c.branches, branch)
	return c.branchErr
}

func (c *fakeCheckout) Commit(_ context.Context, message string) error {
	c.commits = append(c.commits, message)
	return c.commitErr
}

func (c *fakeCheckout) Push(_ context.Context, branch string) error {
	c.pushes = append(c.pushes, branch)
	return c.pushErr
}

func (c *fakeCheckout) Close() error {
	c.closed = true
	return nil
}

// fakeRepository records what the use case asked of the manifest repository.
type fakeRepository struct {
	checkout *fakeCheckout

	checkedOut   []string
	pullRequests []model.PullRequest
	lookups      []string

	checkoutErr error
	prErr       error

	// openPullRequestURL is what the lookup finds. Empty means the branch exists
	// on the remote without a pull request.
	openPullRequestURL string
	lookupErr          error
}

func (r *fakeRepository) FindOpenPullRequest(_ context.Context, _, _, head string) (string, error) {
	r.lookups = append(r.lookups, head)
	if r.lookupErr != nil {
		return "", r.lookupErr
	}
	return r.openPullRequestURL, nil
}

func (r *fakeRepository) Checkout(_ context.Context, repositoryURL string) (model.Checkout, error) {
	r.checkedOut = append(r.checkedOut, repositoryURL)
	if r.checkoutErr != nil {
		return nil, r.checkoutErr
	}
	return r.checkout, nil
}

func (r *fakeRepository) CreatePullRequest(_ context.Context, pr model.PullRequest) (string, error) {
	r.pullRequests = append(r.pullRequests, pr)
	if r.prErr != nil {
		return "", r.prErr
	}
	return "https://github.com/example-org/example-manifests/pull/1", nil
}

// fakePatcher records the update it was handed.
type fakePatcher struct {
	dirs    []string
	updates []model.ImageUpdate
	err     error
}

func (p *fakePatcher) Patch(_ context.Context, dir string, update model.ImageUpdate) error {
	p.dirs = append(p.dirs, dir)
	p.updates = append(p.updates, update)
	return p.err
}

// fakeDirectoryResolver expands configured patterns and leaves literals alone.
type fakeDirectoryResolver struct {
	matches  map[string][]string
	patterns []string
	err      error
}

func (r *fakeDirectoryResolver) Resolve(_ context.Context, _, pattern string) ([]string, error) {
	r.patterns = append(r.patterns, pattern)
	if r.err != nil {
		return nil, r.err
	}
	if matches, exists := r.matches[pattern]; exists {
		return append([]string(nil), matches...), nil
	}
	return []string{pattern}, nil
}

// fakeResolver returns fixed metadata or a failure.
type fakeResolver struct {
	metadata model.ImageMetadata
	err      error
}

func (r fakeResolver) Resolve(context.Context, model.ImageReference) (model.ImageMetadata, error) {
	return r.metadata, r.err
}

// countingResolver records how often the registry was asked.
type countingResolver struct {
	metadata model.ImageMetadata
	calls    int
}

func (r *countingResolver) Resolve(context.Context, model.ImageReference) (model.ImageMetadata, error) {
	r.calls++
	return r.metadata, nil
}

func testRuleSet(t *testing.T) model.RuleSet {
	t.Helper()

	rules, err := model.NewRuleSet([]model.Rule{{
		ImagePattern: testRegistry + "/apps/$1/$2",
		ManifestURL:  "https://github.com/example-org/example-manifests/services/$1/$2/overlays/development",
		Env:          "development",
		DenyTags:     []string{"latest"},
	}})
	require.NoError(t, err)

	return rules
}

func testPushEvent(tag string) model.ImagePushEvent {
	return model.ImagePushEvent{
		Kind:       model.RegistryECR,
		Host:       testRegistry,
		Repository: "apps/platform/image-updater",
		Tag:        tag,
	}
}

func testMetadata() model.ImageMetadata {
	return model.ImageMetadata{
		Labels: map[string]string{
			model.LabelSource:     "https://github.com/example-org/example-app",
			model.LabelRevision:   "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678",
			model.LabelBuildActor: "octocat",
			model.LabelPRAuthor:   "octocat",
		},
		URI:       testRegistry + "/apps/platform/image-updater:" + testTag,
		SizeBytes: 12345,
	}
}

func testMessageRenderer(t *testing.T) *MessageRenderer {
	t.Helper()

	renderer, err := NewMessageRenderer(MessageTemplates{
		PullRequestTitle: "[{{.Environment}}][Image Updater][{{.ImageName}}] Update image",
		PullRequestBody:  "{{.DefaultBody}}",
		CommitMessage:    "[{{.Environment}}][image-committer][{{.ImageRepository}}] Update image",
	})
	require.NoError(t, err)
	return renderer
}

// newService builds the use case over the fakes, returning them for assertions.
func newService(t *testing.T, resolver model.MetadataResolver, opts ...Option) (*Service, *fakeRepository, *fakePatcher) {
	t.Helper()

	repository := &fakeRepository{checkout: &fakeCheckout{dir: t.TempDir()}}
	patcher := &fakePatcher{}

	directories := &fakeDirectoryResolver{}
	service, err := NewService(
		testRuleSet(t),
		resolver,
		repository,
		directories,
		patcher,
		testMessageRenderer(t),
		opts...,
	)
	require.NoError(t, err)

	return service, repository, patcher
}

// newAnnotatingService builds the use case with the label driven annotation on,
// which is what most of the assertions below are about.
func newAnnotatingService(t *testing.T, resolver model.MetadataResolver) (*Service, *fakeRepository, *fakePatcher) {
	t.Helper()
	return newService(t, resolver, WithImageLabelAnnotation(true))
}

func TestService_Handle(t *testing.T) {
	t.Parallel()

	service, repository, patcher := newAnnotatingService(t, fakeResolver{metadata: testMetadata()})
	groupedRules, err := model.NewRuleSet([]model.Rule{
		{
			ImagePattern: testRegistry + "/apps/$1/$2",
			ManifestURL:  "https://github.com/example-org/example-manifests/services/$1/$2/overlays/development",
			Env:          "development",
			DenyTags:     []string{"latest"},
		},
		{
			ImagePattern: testRegistry + "/apps/$1/$2",
			ManifestURL:  "https://github.com/Example-Org/Example-Manifests/clusters/primary/$1/$2",
			Env:          "development",
			DenyTags:     []string{"latest"},
		},
	})
	require.NoError(t, err)
	service.rules = groupedRules
	service.messages, err = NewMessageRenderer(MessageTemplates{
		PullRequestTitle: "[{{.Environment}}][Image Updater][{{.ImageName}}] イメージの更新",
		PullRequestBody:  "## 📦 イメージ更新\\n\\n環境: {{.Environment}}\\n\\n{{.DefaultBody}}",
		CommitMessage:    "[{{.Environment}}][image-committer][{{.ImageRepository}}] イメージの更新",
	})
	require.NoError(t, err)

	require.NoError(t, service.Handle(context.Background(), testPushEvent(testTag)))

	require.Len(t, repository.checkout.branches, 1)
	branch := repository.checkout.branches[0]
	assert.Contains(t, branch, "image_updater_platform_image-updater_development_"+testTag+"_")

	assert.Equal(t, []string{"https://github.com/example-org/example-manifests"}, repository.checkedOut)
	assert.Equal(t,
		[]string{"[development][image-committer][apps/platform/image-updater] イメージの更新"},
		repository.checkout.commits,
	)
	assert.Equal(t, []string{branch}, repository.checkout.pushes)
	assert.True(t, repository.checkout.closed, "the working copy has to be removed")

	require.Len(t, patcher.updates, 2)
	for _, update := range patcher.updates {
		assert.Equal(t, testRegistry+"/apps/platform/image-updater", update.Image)
		assert.Equal(t, testTag, update.NewTag)
		require.NotNil(t, update.Manifest, "the annotation is on, so the image manifest is written")
		assert.Equal(t, "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678", update.Manifest.GitSHA)
	}

	assert.Equal(t,
		[]string{
			filepath.Join(repository.checkout.dir, "clusters/primary/platform/image-updater"),
			filepath.Join(repository.checkout.dir, "services/platform/image-updater/overlays/development"),
		},
		patcher.dirs,
	)

	require.Len(t, repository.pullRequests, 1)
	pr := repository.pullRequests[0]
	assert.Equal(t, "example-org", pr.Owner)
	assert.Equal(t, "example-manifests", pr.Repository)
	assert.Equal(t, branch, pr.Head)
	assert.Equal(t, "[development][Image Updater][platform/image-updater] イメージの更新", pr.Title)
	assert.Equal(t, []string{"octocat"}, pr.Assignees)
	assert.Equal(t, []string{"octocat"}, pr.Reviewers)
	assert.Contains(t, pr.Body, "## 📦 イメージ更新")
	assert.Contains(t, pr.Body, "環境: development")
	assert.Contains(t, pr.Body, "a1b2c3")
}

func TestService_HandleExpandsRecursiveManifestTargets(t *testing.T) {
	t.Parallel()

	service, repository, patcher := newService(t, nil)
	const pattern = "services/platform/image-updater/overlays/**"
	directories := service.directories.(*fakeDirectoryResolver)
	directories.matches = map[string][]string{
		pattern: {
			"services/platform/image-updater/overlays/production",
			"services/platform/image-updater/overlays/development",
		},
	}

	rules, err := model.NewRuleSet([]model.Rule{
		{
			ImagePattern: testRegistry + "/apps/$1/$2",
			ManifestURL:  "https://github.com/example-org/example-manifests/services/$1/$2/overlays/**",
			Env:          "development",
		},
		{
			ImagePattern: testRegistry + "/apps/$1/$2",
			ManifestURL:  "https://github.com/example-org/example-manifests/services/$1/$2/overlays/production",
			Env:          "development",
		},
	})
	require.NoError(t, err)
	service.rules = rules

	require.NoError(t, service.Handle(context.Background(), testPushEvent(testTag)))

	assert.Equal(t, []string{pattern, "services/platform/image-updater/overlays/production"}, directories.patterns)
	assert.Equal(t, []string{
		filepath.Join(repository.checkout.dir, "services/platform/image-updater/overlays/development"),
		filepath.Join(repository.checkout.dir, "services/platform/image-updater/overlays/production"),
	}, patcher.dirs)
	assert.Len(t, patcher.updates, 2, "the explicit and wildcard overlap must be patched once")
	assert.Len(t, repository.checkout.commits, 1)
	assert.Len(t, repository.pullRequests, 1)
}

func TestService_HandleRejectsBeforeTouchingTheRepository(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		event   model.ImagePushEvent
		wantErr error
	}{
		{
			name: "ルールに当たらないイベント",
			event: model.ImagePushEvent{
				Kind:       model.RegistryECR,
				Host:       testRegistry,
				Repository: "other/app",
				Tag:        testTag,
			},
			wantErr: model.ErrNoMatchingRule,
		},
		{
			name:    "denyImageTag のタグ",
			event:   testPushEvent("latest"),
			wantErr: model.ErrImageTagDenied,
		},
		{
			name:    "git ref として使えないタグ",
			event:   testPushEvent("a..b"),
			wantErr: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			service, repository, patcher := newService(t, fakeResolver{metadata: testMetadata()})

			err := service.Handle(context.Background(), tt.event)
			require.Error(t, err)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			}

			assert.False(t, model.IsRetryable(err), "a configuration decision must not be retried")
			assert.Empty(t, repository.checkedOut, "nothing may be cloned before the event is accepted")
			assert.Empty(t, patcher.updates)
		})
	}
}

func TestService_HandleClassifiesFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		setup       func(*fakeRepository, *fakePatcher)
		wantErr     error
		wantRetry   bool
		wantPRCount int
	}{
		{
			name: "クローンの失敗はリトライ",
			setup: func(r *fakeRepository, _ *fakePatcher) {
				r.checkoutErr = errors.New("network unreachable")
			},
			wantRetry: true,
		},
		{
			name: "ブランチ作成の失敗はリトライ",
			setup: func(r *fakeRepository, _ *fakePatcher) {
				r.checkout.branchErr = errors.New("cannot check out")
			},
			wantRetry: true,
		},
		{
			name: "差分なしは終端",
			setup: func(_ *fakeRepository, p *fakePatcher) {
				p.err = model.ErrNoDifference
			},
			wantErr: model.ErrNoDifference,
		},
		{
			name: "対象イメージがなければ終端",
			setup: func(_ *fakeRepository, p *fakePatcher) {
				p.err = model.ErrImageNotManaged
			},
			wantErr: model.ErrImageNotManaged,
		},
		{
			name: "コミットの失敗はリトライ",
			setup: func(r *fakeRepository, _ *fakePatcher) {
				r.checkout.commitErr = errors.New("index locked")
			},
			wantRetry: true,
		},
		{
			name: "同じ更新の PR が既に open なら終端",
			setup: func(r *fakeRepository, _ *fakePatcher) {
				r.checkout.pushErr = model.ErrDuplicatePullRequest
				r.openPullRequestURL = "https://github.com/example-org/example-manifests/pull/7"
			},
			wantErr: model.ErrDuplicatePullRequest,
		},
		{
			name: "PR 検索が失敗したらリトライ",
			setup: func(r *fakeRepository, _ *fakePatcher) {
				r.checkout.pushErr = model.ErrDuplicatePullRequest
				r.lookupErr = errors.New("secondary rate limit")
			},
			wantRetry: true,
		},
		{
			name: "push の失敗はリトライ",
			setup: func(r *fakeRepository, _ *fakePatcher) {
				r.checkout.pushErr = errors.New("remote hung up")
			},
			wantRetry: true,
		},
		{
			name: "PR 作成の失敗はリトライ",
			setup: func(r *fakeRepository, _ *fakePatcher) {
				r.prErr = errors.New("secondary rate limit")
			},
			wantRetry:   true,
			wantPRCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			service, repository, patcher := newService(t, fakeResolver{metadata: testMetadata()})
			tt.setup(repository, patcher)

			err := service.Handle(context.Background(), testPushEvent(testTag))
			require.Error(t, err)

			assert.Equal(t, tt.wantRetry, model.IsRetryable(err))
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			}
			assert.Len(t, repository.pullRequests, tt.wantPRCount)

			if repository.checkoutErr == nil && len(repository.checkedOut) > 0 {
				assert.True(t, repository.checkout.closed, "the working copy has to be removed on failure too")
			}
		})
	}
}

func TestService_HandleContinuesWithoutImageMetadata(t *testing.T) {
	t.Parallel()

	service, repository, patcher := newAnnotatingService(t, fakeResolver{err: errors.New("registry unreachable")})

	require.NoError(t, service.Handle(context.Background(), testPushEvent(testTag)))

	require.Len(t, repository.pullRequests, 1)
	assert.Empty(t, repository.pullRequests[0].Assignees, "there is no build actor to assign")
	require.Len(t, patcher.updates, 1)
	require.NotNil(t, patcher.updates[0].Manifest)
	assert.Empty(t, patcher.updates[0].Manifest.GitSHA)
}

func TestService_HandleWorksWithoutAResolver(t *testing.T) {
	t.Parallel()

	service, repository, patcher := newService(t, nil, WithImageLabelAnnotation(true))

	require.NoError(t, service.Handle(context.Background(), testPushEvent(testTag)))
	assert.Len(t, repository.pullRequests, 1)
	require.Len(t, patcher.updates, 1)
	require.NotNil(t, patcher.updates[0].Manifest, "the block is written, it just has nothing to carry")
	assert.Empty(t, patcher.updates[0].Manifest.GitSHA)
}

// A failure between the push and the pull request creation used to lose the
// update: the branch is on the remote, the next attempt pushes a commit with a
// different hash, and the rejected push was read as "already done".
func TestService_HandleRecoversAnOrphanBranch(t *testing.T) {
	t.Parallel()

	service, repository, _ := newAnnotatingService(t, fakeResolver{metadata: testMetadata()})
	repository.checkout.pushErr = model.ErrDuplicatePullRequest
	repository.openPullRequestURL = "" // the branch is there, the pull request is not

	require.NoError(t, service.Handle(context.Background(), testPushEvent(testTag)))

	const branch = "image_updater_platform_image-updater_development_" + testTag + "_9064d17dcefdcdb5"
	const legacyBranch = "image_updater_platform_image-updater_development_" + testTag
	assert.Equal(t, []string{branch, legacyBranch, branch, legacyBranch}, repository.lookups,
		"current and legacy branches are checked before checkout and after the rejected push")
	require.Len(t, repository.pullRequests, 1, "the pull request has to be opened on the second attempt")
	assert.Equal(t, branch, repository.pullRequests[0].Head)
}

// The label driven annotation is opt-in, so nothing reads the registry unless it
// was asked for.
func TestService_HandleWithoutImageLabelAnnotation(t *testing.T) {
	t.Parallel()

	resolver := &countingResolver{metadata: testMetadata()}
	service, repository, patcher := newService(t, resolver)

	require.NoError(t, service.Handle(context.Background(), testPushEvent(testTag)))

	assert.Zero(t, resolver.calls, "the registry must not be asked for metadata")

	require.Len(t, patcher.updates, 1)
	assert.Nil(t, patcher.updates[0].Manifest, "the metadata comment block must not be written")
	assert.Equal(t, testTag, patcher.updates[0].NewTag, "the tag is still updated")

	require.Len(t, repository.pullRequests, 1)
	pr := repository.pullRequests[0]
	assert.Empty(t, pr.Assignees)
	assert.Empty(t, pr.Reviewers)
	assert.Equal(t, "[development][Image Updater][platform/image-updater] Update image", pr.Title)
	assert.Equal(t, "## 📦 Image Update\n\n", pr.Body, "the body carries only the header")
}

func TestService_HandleSkipsTheImageManifestWhenDisabled(t *testing.T) {
	t.Parallel()

	disabled := false
	rules, err := model.NewRuleSet([]model.Rule{{
		ImagePattern:       testRegistry + "/apps/$1/$2",
		ManifestURL:        "https://github.com/example-org/example-manifests/services/$1/$2/overlays/development",
		Env:                "development",
		WriteImageManifest: &disabled,
	}})
	require.NoError(t, err)

	repository := &fakeRepository{checkout: &fakeCheckout{dir: t.TempDir()}}
	patcher := &fakePatcher{}

	// The annotation is on globally, so this asserts the rule opting out on its own.
	service, err := NewService(
		rules,
		fakeResolver{metadata: testMetadata()},
		repository,
		&fakeDirectoryResolver{},
		patcher,
		testMessageRenderer(t),
		WithImageLabelAnnotation(true),
	)
	require.NoError(t, err)

	require.NoError(t, service.Handle(context.Background(), testPushEvent(testTag)))

	require.Len(t, patcher.updates, 1)
	assert.Nil(t, patcher.updates[0].Manifest)
}

func TestNewService(t *testing.T) {
	t.Parallel()

	rules := testRuleSet(t)
	repository := &fakeRepository{checkout: &fakeCheckout{}}
	directories := &fakeDirectoryResolver{}
	patcher := &fakePatcher{}
	messages := testMessageRenderer(t)

	tests := []struct {
		name        string
		rules       model.RuleSet
		manifests   model.ManifestRepository
		directories model.ManifestDirectoryResolver
		patcher     model.ManifestPatcher
		messages    *MessageRenderer
		wantErr     bool
	}{
		{name: "有効な組み合わせ", rules: rules, manifests: repository, directories: directories, patcher: patcher, messages: messages},
		{name: "ルールが空なら拒否", manifests: repository, directories: directories, patcher: patcher, messages: messages, wantErr: true},
		{name: "manifest repository が nil なら拒否", rules: rules, directories: directories, patcher: patcher, messages: messages, wantErr: true},
		{name: "directory resolver が nil なら拒否", rules: rules, manifests: repository, patcher: patcher, messages: messages, wantErr: true},
		{name: "patcher が nil なら拒否", rules: rules, manifests: repository, directories: directories, messages: messages, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := NewService(tt.rules, nil, tt.manifests, tt.directories, tt.patcher, tt.messages)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestImageName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		repository string
		want       string
	}{
		{name: "先頭の名前空間を落とす", repository: "apps/platform/image-updater", want: "platform/image-updater"},
		{name: "先頭のスラッシュを無視する", repository: "/apps/samples/app", want: "samples/app"},
		{name: "単一セグメントはそのまま", repository: "app", want: "app"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, imageName(tt.repository))
		})
	}
}

func TestValidateBranch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		branch  string
		wantErr bool
	}{
		{name: "通常のブランチ名", branch: "image_updater_app_dev_abc1234"},
		{name: "空は拒否", branch: "", wantErr: true},
		{name: "連続ドットは拒否", branch: "image_updater_app_dev_a..b", wantErr: true},
		{name: "空白は拒否", branch: "image updater", wantErr: true},
		{name: "ハイフン始まりは拒否", branch: "-image_updater", wantErr: true},
		{name: "末尾ドットは拒否", branch: "image_updater.", wantErr: true},
		{name: ".lock 終わりは拒否", branch: "image_updater.lock", wantErr: true},
		{name: "コロンは拒否", branch: "image:updater", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateBranch(tt.branch)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

// The manifest directory has to stay inside the working copy.
func TestService_HandlePatchesInsideTheCheckout(t *testing.T) {
	t.Parallel()

	service, repository, patcher := newService(t, nil)

	require.NoError(t, service.Handle(context.Background(), testPushEvent(testTag)))
	require.Len(t, patcher.dirs, 1)

	root, err := filepath.Abs(repository.checkout.dir)
	require.NoError(t, err)
	target, err := filepath.Abs(patcher.dirs[0])
	require.NoError(t, err)

	relative, err := filepath.Rel(root, target)
	require.NoError(t, err)
	assert.False(t, filepath.IsAbs(relative))
	assert.NotEqual(t, "..", relative)

	_, statErr := os.Stat(root)
	require.NoError(t, statErr)
}
