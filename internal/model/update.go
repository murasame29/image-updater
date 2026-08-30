package model

// ImageUpdate is the change a ManifestPatcher has to apply.
type ImageUpdate struct {
	// Image is the image without a tag, matched against `images[].name`.
	Image string
	// NewTag is the tag to write.
	NewTag string
	// Manifest, when set, is upserted into the well-known image manifest
	// comment block. A nil value leaves the block alone.
	Manifest *ImageManifest
}

// PullRequest describes the pull request opened for an update.
type PullRequest struct {
	// Owner is the account the repository belongs to.
	Owner string
	// Repository is the repository name.
	Repository string
	// Head is the branch holding the update.
	Head string
	// Base is the branch the update is merged into. Empty means the default of
	// the ManifestRepository implementation.
	Base string
	// Title and Body are the pull request description.
	Title string
	Body  string
	// Assignees and Reviewers are best effort: failing to set them does not
	// fail the update.
	Assignees []string
	Reviewers []string
}
