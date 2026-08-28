// Package ruleset loads the rule file that tells the updater which manifests
// belong to which registry repository.
package ruleset

import (
	"errors"
	"fmt"
	"os"

	yaml "go.yaml.in/yaml/v3"

	"github.com/murasame29/image-updater/internal/model"
)

// rule is the on-disk shape of a model.Rule.
//
// These field names are the public configuration contract, so the mapping onto
// the domain type lives here instead of tagging the domain type with YAML keys.
type rule struct {
	RegistryURI      string   `yaml:"registryURI"`
	GitHubRepository string   `yaml:"githubRepository"`
	Env              string   `yaml:"env"`
	AllowImageTag    string   `yaml:"allowImageTag"`
	DenyImageTag     []string `yaml:"denyImageTag"`
	ImageManifest    *bool    `yaml:"imageManifest"`
}

func (r rule) toModel() model.Rule {
	return model.Rule{
		ImagePattern:       r.RegistryURI,
		ManifestURL:        r.GitHubRepository,
		Env:                r.Env,
		AllowTag:           r.AllowImageTag,
		DenyTags:           r.DenyImageTag,
		WriteImageManifest: r.ImageManifest,
	}
}

// Load reads and compiles the rule file at path.
//
// Returns:
//
//	The compiled rule set, or an error when the file cannot be read or holds a
//	rule that could never be used.
func Load(path string) (model.RuleSet, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return model.RuleSet{}, fmt.Errorf("failed to read the rule file %s: %w", path, err)
	}

	rules, err := Parse(data)
	if err != nil {
		return model.RuleSet{}, fmt.Errorf("%s: %w", path, err)
	}

	return rules, nil
}

// Parse compiles a rule file that has already been read.
func Parse(data []byte) (model.RuleSet, error) {
	var file []rule
	if err := yaml.Unmarshal(data, &file); err != nil {
		return model.RuleSet{}, fmt.Errorf("failed to unmarshal the rule file: %w", err)
	}

	if len(file) == 0 {
		return model.RuleSet{}, errors.New("the rule file declares no rule")
	}

	rules := make([]model.Rule, 0, len(file))
	for _, entry := range file {
		rules = append(rules, entry.toModel())
	}

	return model.NewRuleSet(rules)
}
