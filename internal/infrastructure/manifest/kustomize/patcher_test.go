package kustomize

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/kustomize/api/types"

	"github.com/murasame29/image-updater/internal/model"
)

const testImage = "123456789012.dkr.ecr.ap-northeast-1.amazonaws.com/apps/samples/app"

func TestRepositoryPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		image string
		want  string
	}{
		{
			name:  "ECR のイメージ URI",
			image: "123456789012.dkr.ecr.ap-northeast-1.amazonaws.com/apps/samples/demo/app:dev-a",
			want:  "apps/samples/demo/app",
		},
		{
			name:  "タグなしの ECR のイメージ URI",
			image: "123456789012.dkr.ecr.ap-northeast-1.amazonaws.com/apps/samples/demo/app",
			want:  "apps/samples/demo/app",
		},
		{
			name:  "シンプルなイメージ名",
			image: "nginx:latest",
			want:  "nginx",
		},
		{
			name:  "GHCR のイメージ",
			image: "ghcr.io/example-org/example-manifests/app:abc1234",
			want:  "example-org/example-manifests/app",
		},
		{
			name:  "Artifact Registry のイメージ",
			image: "asia-northeast1-docker.pkg.dev/project/repo/app:abc1234",
			want:  "project/repo/app",
		},
		{
			name:  "ポート付きの Harbor のイメージ",
			image: "harbor.example.com:5000/apps/app:abc1234",
			want:  "apps/app",
		},
		{
			name:  "ダイジェスト指定",
			image: "ghcr.io/example-org/app@sha256:0123456789abcdef",
			want:  "example-org/app",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, repositoryPath(tt.image))
		})
	}
}

func TestTagIdentifier(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		tag  string
		want string
	}{
		{name: "コミットハッシュ付きの識別子タグ", tag: "dev-a.790bf3ee04b441a96fb3d1860aea91fa09b72747", want: "dev-a"},
		{name: "大文字を含む識別子タグ", tag: "DEV-B.054da21f89acc2f24e77f2f910b307c3ad85526d", want: "DEV-B"},
		{name: "アプリケーション名を含む識別子タグ", tag: "app-service.0f035a3f345af98984dfe40490e32d69294805c2", want: "app-service"},
		{name: "ドットがないタグ", tag: "latest", want: "latest"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, tagIdentifier(tt.tag))
		})
	}
}

func TestReplaceTag(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		images  []types.Image
		image   string
		newTag  string
		want    []types.Image
		wantErr error
	}{
		{
			name: "複数イメージから特定識別子のイメージだけ更新",
			images: []types.Image{
				{Name: testImage, NewTag: "dev-a.790bf3ee04b441a96fb3d1860aea91fa09b72747"},
				{Name: testImage, NewTag: "dev-b.054da21f89acc2f24e77f2f910b307c3ad85526d"},
				{Name: testImage, NewTag: "dev-c.0f035a3f345af98984dfe40490e32d69294805c2"},
			},
			image:  testImage,
			newTag: "dev-a.new_commit_hash",
			want: []types.Image{
				{Name: testImage, NewTag: "dev-a.new_commit_hash"},
				{Name: testImage, NewTag: "dev-b.054da21f89acc2f24e77f2f910b307c3ad85526d"},
				{Name: testImage, NewTag: "dev-c.0f035a3f345af98984dfe40490e32d69294805c2"},
			},
		},
		{
			name:    "異なるリポジトリの場合は更新しない",
			images:  []types.Image{{Name: testImage, NewTag: "dev-a.790bf3ee04b441a96fb3d1860aea91fa09b72747"}},
			image:   "123456789012.dkr.ecr.ap-northeast-1.amazonaws.com/apps/samples/different-app",
			newTag:  "dev-a.new_commit_hash",
			wantErr: model.ErrImageNotManaged,
		},
		{
			name:    "識別子が一致しない場合は更新しない",
			images:  []types.Image{{Name: testImage, NewTag: "dev-a.790bf3ee04b441a96fb3d1860aea91fa09b72747"}},
			image:   testImage,
			newTag:  "different-prefix.new_commit_hash",
			wantErr: model.ErrImageNotManaged,
		},
		{
			name: "ドットつきのタグに対してドットがないタグの場合は何もしない",
			images: []types.Image{
				{Name: testImage, NewTag: "test-alpha.6b1eedde2a95e893f942d5145769a548bedf0014"},
				{Name: testImage, NewTag: "test-beta.6b1eedde2a95e893f942d5145769a548bedf0014"},
			},
			image:  testImage,
			newTag: "344b507dac223958eee3c90b3b989214679d8597",
			want: []types.Image{
				{Name: testImage, NewTag: "test-alpha.6b1eedde2a95e893f942d5145769a548bedf0014"},
				{Name: testImage, NewTag: "test-beta.6b1eedde2a95e893f942d5145769a548bedf0014"},
			},
		},
		{
			name:   "レジストリホストが違っていてもリポジトリパスで一致させる",
			images: []types.Image{{Name: "999999999999.dkr.ecr.ap-northeast-1.amazonaws.com/apps/samples/app", NewTag: "abc1234"}},
			image:  testImage,
			newTag: "def5678",
			want:   []types.Image{{Name: "999999999999.dkr.ecr.ap-northeast-1.amazonaws.com/apps/samples/app", NewTag: "def5678"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := replaceTag(tt.images, tt.image, tt.newTag)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestChangedNewTags(t *testing.T) {
	t.Parallel()

	doc, err := parse([]byte(kustomizationFixture))
	require.NoError(t, err)

	original := doc.Images()
	updated, err := replaceTag(original, testImage, "bob.9999999999999999999999999999999999999999")
	require.NoError(t, err)

	assert.Equal(t, map[int]string{1: "bob.9999999999999999999999999999999999999999"}, changedNewTags(original, updated))
}

func TestChangedNewTagsIsEmptyWhenNothingMoves(t *testing.T) {
	t.Parallel()

	doc, err := parse([]byte(kustomizationFixture))
	require.NoError(t, err)

	original := doc.Images()
	updated, err := replaceTag(original, testImage, "alice.790bf3ee04b441a96fb3d1860aea91fa09b72747")
	require.NoError(t, err)

	assert.Empty(t, changedNewTags(original, updated))
}

func TestPatcher_Patch(t *testing.T) {
	t.Parallel()

	manifest := testManifest(testImage, "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678")

	tests := []struct {
		name     string
		source   string
		update   model.ImageUpdate
		wantErr  error
		contains []string
	}{
		{
			name:   "タグを更新してメタデータブロックを書く",
			source: kustomizationFixture,
			update: model.ImageUpdate{Image: testImage, NewTag: "alice.1111111111111111111111111111111111111111", Manifest: &manifest},
			contains: []string{
				"    newTag: alice.1111111111111111111111111111111111111111 # inline comment\n",
				"# " + model.ImageManifestBeginMarker + "\n",
			},
		},
		{
			name:   "manifest が nil ならメタデータブロックは書かない",
			source: kustomizationFixture,
			update: model.ImageUpdate{Image: testImage, NewTag: "alice.1111111111111111111111111111111111111111"},
			contains: []string{
				"    newTag: alice.1111111111111111111111111111111111111111 # inline comment\n",
			},
		},
		{
			name:    "すでに同じタグなら ErrNoDifference",
			source:  kustomizationFixture,
			update:  model.ImageUpdate{Image: testImage, NewTag: "alice.790bf3ee04b441a96fb3d1860aea91fa09b72747"},
			wantErr: model.ErrNoDifference,
		},
		{
			name:    "参照されていないイメージなら ErrImageNotManaged",
			source:  kustomizationFixture,
			update:  model.ImageUpdate{Image: "ghcr.io/example-org/other", NewTag: "abc1234"},
			wantErr: model.ErrImageNotManaged,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			path := filepath.Join(dir, FileName)
			require.NoError(t, os.WriteFile(path, []byte(tt.source), 0o644))

			err := NewPatcher().Patch(context.Background(), dir, tt.update)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)

				// A rejected update must leave the file untouched.
				after, readErr := os.ReadFile(path)
				require.NoError(t, readErr)
				assert.Equal(t, tt.source, string(after))
				return
			}
			require.NoError(t, err)

			after, err := os.ReadFile(path)
			require.NoError(t, err)
			for _, want := range tt.contains {
				assert.Contains(t, string(after), want)
			}

			if tt.update.Manifest == nil {
				assert.NotContains(t, string(after), model.ImageManifestBeginMarker)
			}
		})
	}
}

func TestPatcher_PatchReportsAMissingFile(t *testing.T) {
	t.Parallel()

	err := NewPatcher().Patch(context.Background(), t.TempDir(), model.ImageUpdate{Image: testImage, NewTag: "abc1234"})
	require.ErrorIs(t, err, model.ErrManifestNotFound)
}

func TestPatcher_PatchKeepsTheFileMode(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	require.NoError(t, os.WriteFile(path, []byte(kustomizationFixture), 0o600))

	require.NoError(t, NewPatcher().Patch(context.Background(), dir, model.ImageUpdate{
		Image:  testImage,
		NewTag: "alice.1111111111111111111111111111111111111111",
	}))

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}
