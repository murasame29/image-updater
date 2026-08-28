package ruleset

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/murasame29/image-updater/internal/model"
)

func TestLoad(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		path      string
		wantRules int
		wantErr   bool
	}{
		{name: "ルールファイルを読み込む", path: "testdata/rules.yaml", wantRules: 3},
		{name: "重なるルールも読み込む", path: "testdata/rules-overlapping.yaml", wantRules: 3},
		{name: "存在しないファイルはエラー", path: "testdata/missing.yaml", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rules, err := Load(tt.path)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantRules, rules.Len())
		})
	}
}

func TestLoadMapsTheConfigurationKeys(t *testing.T) {
	t.Parallel()

	rules, err := Load("testdata/rules.yaml")
	require.NoError(t, err)

	matched, err := rules.Match(model.ImagePushEvent{
		Kind:       model.RegistryECR,
		Host:       "123456789012.dkr.ecr.ap-northeast-1.amazonaws.com",
		Repository: "apps/alpha/beta/app",
		Tag:        "1f569a438ad04ca4c6f5d7fcf78391870d1a5e80",
	})
	require.NoError(t, err)

	rule := matched.Rule()
	assert.Equal(t, "123456789012.dkr.ecr.ap-northeast-1.amazonaws.com/apps/$1/$2/$3", rule.ImagePattern)
	assert.Equal(t, "https://github.com/example-org/example-manifests/services/$1/$2/$3/overlays/development", rule.ManifestURL)
	assert.Equal(t, []string{"latest", "main"}, rule.DenyTags)
	assert.True(t, matched.WritesImageManifest())
	require.NoError(t, matched.ValidateTag())
}

func TestParse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		source  string
		wantErr bool
	}{
		{
			name: "最小構成",
			source: "" +
				"- registryURI: 123456789012.dkr.ecr.ap-northeast-1.amazonaws.com/apps/$1\n" +
				"  githubRepository: https://github.com/example-org/example-manifests/services/$1\n",
		},
		{
			name:    "空のファイルは拒否",
			source:  "[]\n",
			wantErr: true,
		},
		{
			name:    "YAML として壊れていれば拒否",
			source:  "- registryURI: [\n",
			wantErr: true,
		},
		{
			name: "使えない正規表現は起動時に拒否",
			source: "" +
				"- registryURI: 123456789012.dkr.ecr.ap-northeast-1.amazonaws.com/apps/$1\n" +
				"  githubRepository: https://github.com/example-org/example-manifests/services/$1\n" +
				"  allowImageTag: 'regexp:^[0-9a-f{7,40}$'\n",
			wantErr: true,
		},
		{
			name: "manifest URL がなければ拒否",
			source: "" +
				"- registryURI: 123456789012.dkr.ecr.ap-northeast-1.amazonaws.com/apps/$1\n",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := Parse([]byte(tt.source))
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestParseReadsImageManifestFlag(t *testing.T) {
	t.Parallel()

	rules, err := Parse([]byte("" +
		"- registryURI: 123456789012.dkr.ecr.ap-northeast-1.amazonaws.com/apps/$1\n" +
		"  githubRepository: https://github.com/example-org/example-manifests/services/$1\n" +
		"  imageManifest: false\n"))
	require.NoError(t, err)

	matched, err := rules.Match(model.ImagePushEvent{
		Host:       "123456789012.dkr.ecr.ap-northeast-1.amazonaws.com",
		Repository: "apps/app",
		Tag:        "abcdef1",
	})
	require.NoError(t, err)
	assert.False(t, matched.WritesImageManifest())
}
