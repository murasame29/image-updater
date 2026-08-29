// Package kustomize locates and updates image tags in kustomization.yaml files.
//
// It implements the manifest directory resolver and patcher ports: nothing
// outside this package knows that the manifests happen to be kustomize overlays.
package kustomize

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"sigs.k8s.io/kustomize/api/types"

	"github.com/murasame29/image-updater/internal/model"
)

// FileName is the manifest the patcher edits.
const (
	FileName        = "kustomization.yaml"
	maxManifestSize = 8 << 20
)

var errFileTooLarge = errors.New("file too large")

// Patcher rewrites the images block of a kustomization.yaml.
type Patcher struct {
	// indent is the indent of the YAML rendered inside the managed metadata
	// comment block. It does not affect the rest of the file, which is edited
	// line by line and keeps whatever style it already had.
	indent int
}

// Option tunes a Patcher.
type Option func(*Patcher)

// WithIndent sets the indent of the managed metadata comment block. A value the
// YAML emitter does not honour is ignored in favour of the default.
func WithIndent(indent int) Option {
	return func(p *Patcher) {
		if model.IsValidImageManifestIndent(indent) {
			p.indent = indent
		}
	}
}

var (
	_ model.ManifestDirectoryResolver = Patcher{}
	_ model.ManifestPatcher           = Patcher{}
)

// NewPatcher builds a patcher indenting the metadata block by two spaces, which
// is what kustomize itself emits.
func NewPatcher(opts ...Option) Patcher {
	patcher := Patcher{indent: model.ImageManifestIndentDefault}

	for _, opt := range opts {
		opt(&patcher)
	}

	return patcher
}

// Resolve expands one optional ** segment in pattern to every directory that
// contains a regular kustomization.yaml. Literal paths are returned unchanged
// so Patch remains the source of their existing not-found diagnostics.
func (Patcher) Resolve(ctx context.Context, checkoutRoot, pattern string) ([]string, error) {
	const recursiveWildcard = "**"

	segments := splitManifestPath(pattern)
	wildcard := -1
	for i, segment := range segments {
		if segment == recursiveWildcard {
			wildcard = i
			break
		}
	}
	if wildcard < 0 {
		if err := rejectSymlinkPath(checkoutRoot, pattern); err != nil {
			return nil, err
		}
		return []string{pattern}, nil
	}

	prefix := strings.Join(segments[:wildcard], "/")
	if err := rejectSymlinkPath(checkoutRoot, prefix); err != nil {
		return nil, err
	}
	walkRoot := filepath.Join(checkoutRoot, filepath.FromSlash(prefix))
	matches := make([]string, 0)

	err := filepath.WalkDir(walkRoot, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Name() != FileName || entry.Type()&os.ModeSymlink != 0 {
			return nil
		}

		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}

		relative, err := filepath.Rel(checkoutRoot, filepath.Dir(current))
		if err != nil {
			return err
		}
		if filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return fmt.Errorf("%w: manifest path %s escapes checkout %s", model.ErrInvalidManifest, current, checkoutRoot)
		}

		directory := filepath.ToSlash(relative)
		if directory == "." {
			directory = ""
		}
		if matchesRecursiveManifestPath(segments, splitManifestPath(directory), wildcard) {
			if err := rejectSymlinkPath(checkoutRoot, directory); err != nil {
				return err
			}
			matches = append(matches, directory)
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: no manifest directory matches %q", model.ErrManifestNotFound, pattern)
		}
		return nil, fmt.Errorf("walk manifest directories for %q: %w", pattern, err)
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("%w: no manifest directory matches %q", model.ErrManifestNotFound, pattern)
	}

	sort.Strings(matches)
	return matches, nil
}

func rejectSymlinkPath(checkoutRoot, relative string) error {
	converted := filepath.FromSlash(relative)
	if filepath.IsAbs(converted) {
		return fmt.Errorf("%w: manifest path %q is absolute", model.ErrInvalidManifest, relative)
	}
	cleaned := filepath.Clean(converted)
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%w: manifest path %q escapes the checkout", model.ErrInvalidManifest, relative)
	}

	current := filepath.Clean(checkoutRoot)
	for _, segment := range splitManifestPath(filepath.ToSlash(cleaned)) {
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return fmt.Errorf("inspect manifest path %s: %w", current, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: manifest path component %s is a symlink", model.ErrInvalidManifest, current)
		}
	}
	return nil
}

func splitManifestPath(value string) []string {
	value = strings.Trim(value, "/")
	if value == "" || value == "." {
		return nil
	}
	return strings.Split(value, "/")
}

func matchesRecursiveManifestPath(pattern, candidate []string, wildcard int) bool {
	fixedSegments := len(pattern) - 1
	if len(candidate) < fixedSegments {
		return false
	}
	for i := 0; i < wildcard; i++ {
		if pattern[i] != candidate[i] {
			return false
		}
	}

	suffixLength := len(pattern) - wildcard - 1
	for i := 1; i <= suffixLength; i++ {
		if pattern[len(pattern)-i] != candidate[len(candidate)-i] {
			return false
		}
	}
	return true
}

// Patch updates the tag of update.Image inside dir and, when asked for, refreshes
// the well-known image manifest comment block.
//
// The file is rewritten in place only when something actually changed, so a
// redelivered event does not produce an empty commit.
//
// Returns:
//
//	nil when the file was rewritten, ErrManifestNotFound when there is no
//	kustomization.yaml, ErrImageNotManaged when it does not reference the image
//	and ErrNoDifference when it already carries the new tag.
func (p Patcher) Patch(ctx context.Context, dir string, update model.ImageUpdate) error {
	path := filepath.Join(dir, FileName)

	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: %s", model.ErrManifestNotFound, path)
		}
		return fmt.Errorf("failed to inspect %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("%w: %s is not a regular file", model.ErrInvalidManifest, path)
	}

	if info.Size() > maxManifestSize {
		return fmt.Errorf("%w: %s is too large: %d bytes exceeds the %d byte limit",
			model.ErrInvalidManifest, path, info.Size(), maxManifestSize)
	}

	data, err := readFileLimited(path, maxManifestSize)
	if err != nil {
		if errors.Is(err, errFileTooLarge) {
			return fmt.Errorf("%w: %s: %v", model.ErrInvalidManifest, path, err)
		}
		return fmt.Errorf("failed to read %s: %w", path, err)
	}

	doc, err := parse(data)
	if err != nil {
		return fmt.Errorf("%w: failed to parse %s: %v", model.ErrInvalidManifest, path, err)
	}

	original := doc.Images()
	updated, err := replaceTag(original, update.Image, update.NewTag)
	if err != nil {
		return err
	}

	changed := changedNewTags(original, updated)
	if len(changed) == 0 {
		return fmt.Errorf("%w: %s already points at %s", model.ErrNoDifference, FileName, update.NewTag)
	}

	if err := doc.setNewTags(changed); err != nil {
		return fmt.Errorf("%w: failed to update the image tags: %v", model.ErrInvalidManifest, err)
	}

	if update.Manifest != nil {
		// The metadata block is a convenience for humans reading the manifests,
		// so a failure to render it must not hold back the tag update.
		if err := doc.upsertImageManifest(*update.Manifest, p.indent); err != nil {
			slog.WarnContext(ctx, "failed to write the image manifest metadata",
				slog.String("error.message", err.Error()),
			)
		}
	}

	if err := os.WriteFile(path, doc.bytes(), info.Mode().Perm()); err != nil {
		return fmt.Errorf("failed to write %s: %w", path, err)
	}

	slog.DebugContext(ctx, "patched the manifests",
		slog.String("manifest.path", path),
		slog.Int("manifest.images.changed", len(changed)),
	)

	return nil
}

func readFileLimited(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%w: file exceeds the %d byte limit", errFileTooLarge, limit)
	}

	return data, nil
}

// replaceTag sets newTag on the images block entries that refer to image.
//
// Entries are matched on the repository path only, ignoring the registry host:
// the same repository is mirrored per environment under a different account or
// project, so the manifests of an overlay may well name a registry other than
// the one the event came from.
//
// A tag carrying a dot is treated as `<identifier>.<revision>`, where the
// identifier selects which of several entries of the same repository is updated.
// That is how one overlay tracks several branches of the same image.
//
// Returns:
//
//	The updated entries, or ErrImageNotManaged when none refer to the image.
func replaceTag(images []types.Image, image, newTag string) ([]types.Image, error) {
	target := repositoryPath(image)

	// Only a tag with a dot carries an identifier to match on.
	pushedPrefix, hasIdentifier := "", strings.Contains(newTag, tagPieceSeparator)
	if hasIdentifier {
		pushedPrefix = tagIdentifier(newTag)
	}

	found := false
	updated := make([]types.Image, 0, len(images))

	for _, entry := range images {
		if repositoryPath(entry.Name) != target {
			updated = append(updated, entry)
			continue
		}

		switch {
		case hasIdentifier:
			if tagIdentifier(entry.NewTag) == pushedPrefix {
				entry.NewTag = newTag
				found = true
			}
		case entry.NewTag == newTag:
			// Already there: the image is managed, nothing to change.
			found = true
		case !strings.Contains(entry.NewTag, tagPieceSeparator):
			// A plain tag replaces a plain tag. An entry carrying an identifier
			// belongs to another branch and is left alone.
			entry.NewTag = newTag
			found = true
		default:
			found = true
		}

		updated = append(updated, entry)
	}

	if !found {
		return nil, fmt.Errorf("%w: no %s entry refers to %s", model.ErrImageNotManaged, FileName, image)
	}

	return updated, nil
}

const tagPieceSeparator = "."

// repositoryPath strips the registry host, the tag and the digest from an image
// name, leaving the repository path.
func repositoryPath(image string) string {
	name, _, _ := strings.Cut(image, "@")

	segments := strings.Split(name, "/")
	if len(segments) > 1 && isRegistryHost(segments[0]) {
		segments = segments[1:]
	}

	last := len(segments) - 1
	if index := strings.IndexByte(segments[last], ':'); index >= 0 {
		segments[last] = segments[last][:index]
	}

	return strings.Join(segments, "/")
}

// isRegistryHost reports whether a first path segment names a registry rather
// than a repository, using the same rule as a container runtime: a host carries
// a dot or a port, or is localhost.
func isRegistryHost(segment string) bool {
	return strings.ContainsAny(segment, ".:") || segment == "localhost"
}

// tagIdentifier is the piece of a tag before the first dot, which is the branch
// or developer identifier the build pipeline puts there.
//
//	dev-a.790bf3ee04b441a96fb3d1860aea91fa09b72747 -> dev-a
func tagIdentifier(tag string) string {
	identifier, _, _ := strings.Cut(tag, tagPieceSeparator)
	return identifier
}

// changedNewTags returns the images block indexes whose newTag differs.
func changedNewTags(original, updated []types.Image) map[int]string {
	changed := make(map[int]string)
	for i := range updated {
		if i >= len(original) {
			break
		}
		if original[i].NewTag != updated[i].NewTag {
			changed[i] = updated[i].NewTag
		}
	}
	return changed
}
