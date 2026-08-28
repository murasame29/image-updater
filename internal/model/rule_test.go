package model

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testRegistry = "123456789012.dkr.ecr.ap-northeast-1.amazonaws.com"

// testRules mirrors the rule file shipped as the ruleset fixture.
func testRules(t *testing.T) RuleSet {
	t.Helper()

	rules, err := NewRuleSet([]Rule{
		{
			ImagePattern: testRegistry + "/apps/$1/$2/$3",
			ManifestURL:  "https://github.com/example-org/example-manifests/services/$1/$2/$3/overlays/development",
			DenyTags:     []string{"latest", "main"},
		},
		{
			ImagePattern: testRegistry + "/apps/internal/tools/$1:$2.*",
			ManifestURL:  "https://github.com/example-org/example-manifests/services/internal-tools/$2/$1/overlays/development",
			AllowTag:     "regexp:^[0-9a-f]{7,40}$",
			DenyTags:     []string{"latest"},
		},
		{
			ImagePattern: testRegistry + "/apps/multi-tenant/tools/$1:$2.$3.*",
			ManifestURL:  "https://github.com/example-org/example-manifests/services/internal-tools/$2/$1/$3/overlays/development",
			DenyTags:     []string{"latest"},
		},
	})
	require.NoError(t, err)

	return rules
}

func testEvent(repository, tag string) ImagePushEvent {
	return ImagePushEvent{
		Kind:       RegistryECR,
		Host:       testRegistry,
		Repository: repository,
		Tag:        tag,
	}
}

func TestNewRuleSet(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		rule    Rule
		wantErr bool
	}{
		{
			name: "有効なルール",
			rule: Rule{
				ImagePattern: testRegistry + "/apps/$1",
				ManifestURL:  "https://github.com/example-org/example-manifests/services/$1",
			},
		},
		{
			name:    "レジストリホストだけのパターンは拒否",
			rule:    Rule{ImagePattern: testRegistry, ManifestURL: "https://github.com/a/b"},
			wantErr: true,
		},
		{
			name:    "manifest URL が空なら拒否",
			rule:    Rule{ImagePattern: testRegistry + "/apps/$1"},
			wantErr: true,
		},
		{
			name: "コンパイルできない正規表現は起動時に拒否",
			rule: Rule{
				ImagePattern: testRegistry + "/apps/$1",
				ManifestURL:  "https://github.com/a/b",
				AllowTag:     "regexp:[",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := NewRuleSet([]Rule{tt.rule})
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestRuleSet_Match(t *testing.T) {
	t.Parallel()

	overlapping, err := NewRuleSet([]Rule{
		{
			ImagePattern: testRegistry + "/apps/$1/$2/$3",
			ManifestURL:  "https://github.com/example-org/example-manifests/services/$1/$2/$3/overlays/development",
			AllowTag:     "regexp:^[0-9a-f]{7,40}$",
			DenyTags:     []string{"latest"},
		},
		{
			ImagePattern: testRegistry + "/apps/alpha/$1/$2",
			ManifestURL:  "https://github.com/example-org/example-manifests/services/alpha/$1/$2/overlays/development",
			AllowTag:     "regexp:^[0-9a-f]{7,40}$",
			DenyTags:     []string{"latest"},
		},
	})
	require.NoError(t, err)

	tests := []struct {
		name        string
		event       ImagePushEvent
		wantPattern string
		wantErr     error
	}{
		{
			name:        "リテラルが一致する数が多いルールが勝つ",
			event:       testEvent("/apps/alpha/beta/app", "2c26b46b68ffc68ff99b453c1d30413413422d70"),
			wantPattern: testRegistry + "/apps/alpha/$1/$2",
		},
		{
			name:        "より具体的なルールに当たらなければ汎用のルールが使われる",
			event:       testEvent("/apps/gamma/beta/app", "2c26b46b68ffc68ff99b453c1d30413413422d70"),
			wantPattern: testRegistry + "/apps/$1/$2/$3",
		},
		{
			name:    "セグメント数が足りないリポジトリはどのルールにも当たらない",
			event:   testEvent("apps/gamma/beta", "2c26b46b68ffc68ff99b453c1d30413413422d70"),
			wantErr: ErrNoMatchingRule,
		},
		{
			name:    "セグメント数が多すぎるリポジトリはどのルールにも当たらない",
			event:   testEvent("apps/alpha/beta/app/extra", "2c26b46b68ffc68ff99b453c1d30413413422d70"),
			wantErr: ErrNoMatchingRule,
		},
		{
			name:    "レジストリホストが違えばどのルールにも当たらない",
			event:   ImagePushEvent{Host: "999999999999.dkr.ecr.ap-northeast-1.amazonaws.com", Repository: "apps/alpha/beta/app", Tag: "abc1234"},
			wantErr: ErrNoMatchingRule,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			matched, err := overlapping.Match(tt.event)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantPattern, matched.Rule().ImagePattern)
		})
	}
}

func TestMatchedRule_ValidateTag(t *testing.T) {
	t.Parallel()

	rules := testRules(t)

	tests := []struct {
		name       string
		repository string
		tag        string
		wantErr    error
	}{
		{
			name:       "許可も拒否もされていないタグは通る",
			repository: "apps/alpha/beta/app",
			tag:        "1f569a438ad04ca4c6f5d7fcf78391870d1a5e80",
		},
		{
			name:       "denyImageTag の latest は拒否",
			repository: "apps/alpha/beta/app",
			tag:        "latest",
			wantErr:    ErrImageTagDenied,
		},
		{
			name:       "denyImageTag の main は拒否",
			repository: "apps/alpha/beta/app",
			tag:        "main",
			wantErr:    ErrImageTagDenied,
		},
		{
			name:       "allowImageTag の正規表現に一致すれば通る",
			repository: "apps/internal/tools/app",
			tag:        "1f569a438ad04ca4c6f5d7fcf78391870d1a5e80",
		},
		{
			name:       "allowImageTag の正規表現に一致しなければ拒否",
			repository: "apps/internal/tools/app",
			tag:        "web-demo.1f569a438ad04ca4c6f5d7fcf78391870d1a5e80",
			wantErr:    ErrImageTagNotAllowed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			matched, err := rules.Match(testEvent(tt.repository, tt.tag))
			require.NoError(t, err)

			err = matched.ValidateTag()
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestMatchedRule_Location(t *testing.T) {
	t.Parallel()

	rules := testRules(t)

	tests := []struct {
		name           string
		repository     string
		tag            string
		wantRepository string
		wantOwner      string
		wantName       string
		wantDir        string
		wantErr        error
	}{
		{
			name:           "リポジトリパスの捕捉を展開する",
			repository:     "/apps/alpha/beta/app",
			tag:            "2c26b46b68ffc68ff99b453c1d30413413422d70",
			wantRepository: "https://github.com/example-org/example-manifests",
			wantOwner:      "example-org",
			wantName:       "example-manifests",
			wantDir:        "services/alpha/beta/app/overlays/development",
		},
		{
			name:           "タグの捕捉も展開する",
			repository:     "apps/internal/tools/app",
			tag:            "web-demo.1f569a438ad04ca4c6f5d7fcf78391870d1a5e80",
			wantRepository: "https://github.com/example-org/example-manifests",
			wantOwner:      "example-org",
			wantName:       "example-manifests",
			wantDir:        "services/internal-tools/web-demo/app/overlays/development",
		},
		{
			name:           "タグの捕捉が複数あっても展開する",
			repository:     "apps/multi-tenant/tools/app",
			tag:            "blue.beta.1f569a438ad04ca4c6f5d7fcf78391870d1a5e80",
			wantRepository: "https://github.com/example-org/example-manifests",
			wantOwner:      "example-org",
			wantName:       "example-manifests",
			wantDir:        "services/internal-tools/blue/app/beta/overlays/development",
		},
		{
			name:       "タグのピースが足りなければ ErrIncompleteRule",
			repository: "apps/multi-tenant/tools/app",
			tag:        "abcdef1",
			wantErr:    ErrIncompleteRule,
		},
		{
			// The trailing * of the tag pattern does not demand a piece of its
			// own, so two pieces are enough for $2.$3.*
			name:           "末尾のワイルドカードはピースを要求しない",
			repository:     "apps/multi-tenant/tools/app",
			tag:            "blue.beta",
			wantRepository: "https://github.com/example-org/example-manifests",
			wantOwner:      "example-org",
			wantName:       "example-manifests",
			wantDir:        "services/internal-tools/blue/app/beta/overlays/development",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			matched, err := rules.Match(testEvent(tt.repository, tt.tag))
			require.NoError(t, err)

			location, err := matched.Location()
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)

			assert.Equal(t, tt.wantRepository, location.RepositoryURL)
			assert.Equal(t, tt.wantOwner, location.Owner)
			assert.Equal(t, tt.wantName, location.Repository)
			assert.Equal(t, tt.wantDir, location.Dir)
		})
	}
}

func TestMatchedRule_LocationRejectsEscapingPaths(t *testing.T) {
	t.Parallel()

	rules, err := NewRuleSet([]Rule{{
		ImagePattern: testRegistry + "/apps/$1",
		ManifestURL:  "https://github.com/example-org/example-manifests/$1/../../../etc",
	}})
	require.NoError(t, err)

	matched, err := rules.Match(testEvent("apps/app", "abcdef1"))
	require.NoError(t, err)

	_, err = matched.Location()
	require.ErrorIs(t, err, ErrIncompleteRule)
}

func TestMatchedRule_WritesImageManifest(t *testing.T) {
	t.Parallel()

	disabled := false

	tests := []struct {
		name string
		flag *bool
		want bool
	}{
		{name: "未指定なら有効", flag: nil, want: true},
		{name: "false なら無効", flag: &disabled, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rules, err := NewRuleSet([]Rule{{
				ImagePattern:       testRegistry + "/apps/$1",
				ManifestURL:        "https://github.com/example-org/example-manifests/services/$1",
				WriteImageManifest: tt.flag,
			}})
			require.NoError(t, err)

			matched, err := rules.Match(testEvent("apps/app", "abcdef1"))
			require.NoError(t, err)
			assert.Equal(t, tt.want, matched.WritesImageManifest())
		})
	}
}

func TestExpandVariables(t *testing.T) {
	t.Parallel()

	// $10 must not be corrupted by the substitution of $1.
	got := expandVariables("a/$1/$10", map[string]string{"$1": "one", "$10": "ten"})
	assert.Equal(t, "a/one/ten", got)
}

func TestRetryable(t *testing.T) {
	t.Parallel()

	assert.NoError(t, Retryable(nil))

	wrapped := Retryable(ErrNoDifference)
	assert.True(t, IsRetryable(wrapped))
	assert.True(t, errors.Is(wrapped, ErrNoDifference))
	assert.False(t, IsRetryable(ErrNoDifference))
}
