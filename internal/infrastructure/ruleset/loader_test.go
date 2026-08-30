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
	assert.Equal(t, "development", matched.Env())
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
				"  githubRepository: https://github.com/example-org/example-manifests/services/$1\n" +
				"  environment: development\n",
		},
		{
			name: "旧 env キーは拒否",
			source: "" +
				"- registryURI: 123456789012.dkr.ecr.ap-northeast-1.amazonaws.com/apps/$1\n" +
				"  githubRepository: https://github.com/example-org/example-manifests/services/$1\n" +
				"  env: development\n",
			wantErr: true,
		},
		{
			name: "environment がなければ拒否",
			source: "" +
				"- registryURI: 123456789012.dkr.ecr.ap-northeast-1.amazonaws.com/apps/$1\n" +
				"  githubRepository: https://github.com/example-org/example-manifests/services/$1\n",
			wantErr: true,
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
				"  environment: development\n" +
				"  allowImageTag: 'regexp:^[0-9a-f{7,40}$'\n",
			wantErr: true,
		},
		{
			name: "manifest URL がなければ拒否",
			source: "" +
				"- registryURI: 123456789012.dkr.ecr.ap-northeast-1.amazonaws.com/apps/$1\n" +
				"  environment: development\n",
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
		"  environment: development\n" +
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

func TestParseFileReadsStructuredConfiguration(t *testing.T) {
	t.Parallel()

	file, err := ParseFile([]byte(`messages:
  pullRequestTitle: '[{{.Environment}}][Image Updater][{{.ImageName}}] イメージの更新'
  pullRequestBody: |-
    ## イメージの更新

    {{.DefaultBody}}
  commitMessage: '[{{.Environment}}][image-committer][{{.ImageRepository}}] イメージの更新'
rules:
  - registryURI: 123456789012.dkr.ecr.ap-northeast-1.amazonaws.com/apps/$1
    githubRepository: https://github.com/example-org/example-manifests/services/$1
    environment: production
`))
	require.NoError(t, err)
	assert.Equal(t, 1, file.RuleSet.Len())
	require.NotNil(t, file.Messages.PullRequestTitle)
	require.NotNil(t, file.Messages.PullRequestBody)
	require.NotNil(t, file.Messages.CommitMessage)
	assert.Contains(t, *file.Messages.PullRequestTitle, "イメージの更新")
	assert.Contains(t, *file.Messages.PullRequestBody, "{{.DefaultBody}}")
	assert.Contains(t, *file.Messages.CommitMessage, "イメージの更新")
}
