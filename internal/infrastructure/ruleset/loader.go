// Package ruleset loads the configuration file that tells the updater which
// manifests belong to which registry repository.
package ruleset

import (
	"errors"
	"fmt"
	"os"
	"strings"

	yaml "go.yaml.in/yaml/v3"

	"github.com/murasame29/image-updater/internal/model"
)

// MessageTemplates is the on-disk shape of the user-facing message settings.
// Pointer fields distinguish an omitted value from an explicitly empty value,
// which the application rejects during startup validation.
type MessageTemplates struct {
	PullRequestTitle *string `yaml:"pullRequestTitle"`
	PullRequestBody  *string `yaml:"pullRequestBody"`
	CommitMessage    *string `yaml:"commitMessage"`
}

// FileConfig is the compiled configuration loaded from one YAML snapshot.
type FileConfig struct {
	RuleSet  model.RuleSet
	Messages MessageTemplates
}

// rule is the on-disk shape of a model.Rule.
//
// These field names are the public configuration contract, so the mapping onto
// the domain type lives here instead of tagging the domain type with YAML keys.
type rule struct {
	RegistryURI      string    `yaml:"registryURI"`
	GitHubRepository string    `yaml:"githubRepository"`
	Environment      string    `yaml:"environment"`
	LegacyEnv        yaml.Node `yaml:"env"`
	AllowImageTag    string    `yaml:"allowImageTag"`
	DenyImageTag     []string  `yaml:"denyImageTag"`
	ImageManifest    *bool     `yaml:"imageManifest"`
}

type fileDocument struct {
	Messages MessageTemplates `yaml:"messages"`
	Rules    []rule           `yaml:"rules"`
}

func (r rule) toModel() (model.Rule, error) {
	if r.LegacyEnv.Kind != 0 {
		return model.Rule{}, errors.New(`"env" is not supported; use "environment"`)
	}
	if strings.TrimSpace(r.Environment) == "" {
		return model.Rule{}, errors.New(`"environment" is required`)
	}

	return model.Rule{
		ImagePattern:       r.RegistryURI,
		ManifestURL:        r.GitHubRepository,
		Env:                r.Environment,
		AllowTag:           r.AllowImageTag,
		DenyTags:           r.DenyImageTag,
		WriteImageManifest: r.ImageManifest,
	}, nil
}

// LoadFile reads and compiles the complete configuration file at path.
func LoadFile(path string) (FileConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return FileConfig{}, fmt.Errorf("failed to read the configuration file %s: %w", path, err)
	}

	file, err := ParseFile(data)
	if err != nil {
		return FileConfig{}, fmt.Errorf("%s: %w", path, err)
	}

	return file, nil
}

// ParseFile compiles a configuration file that has already been read. It
// accepts both the current mapping format and the legacy root rule sequence.
func ParseFile(data []byte) (FileConfig, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return FileConfig{}, fmt.Errorf("failed to unmarshal the configuration file: %w", err)
	}
	if len(document.Content) == 0 {
		return FileConfig{}, errors.New("the configuration file declares no rule")
	}

	root := document.Content[0]
	var entries []rule
	var messages MessageTemplates

	switch root.Kind {
	case yaml.SequenceNode:
		if err := root.Decode(&entries); err != nil {
			return FileConfig{}, fmt.Errorf("failed to decode the legacy rule list: %w", err)
		}
	case yaml.MappingNode:
		var file fileDocument
		if err := root.Decode(&file); err != nil {
			return FileConfig{}, fmt.Errorf("failed to decode the configuration document: %w", err)
		}
		entries = file.Rules
		messages = file.Messages
	default:
		return FileConfig{}, errors.New("the configuration root must be a mapping or a rule sequence")
	}

	if len(entries) == 0 {
		return FileConfig{}, errors.New("the configuration file declares no rule")
	}

	rules := make([]model.Rule, 0, len(entries))
	for i, entry := range entries {
		rule, err := entry.toModel()
		if err != nil {
			return FileConfig{}, fmt.Errorf("rule %d: %w", i, err)
		}
		rules = append(rules, rule)
	}

	compiled, err := model.NewRuleSet(rules)
	if err != nil {
		return FileConfig{}, err
	}

	return FileConfig{RuleSet: compiled, Messages: messages}, nil
}

// Load reads and compiles only the rules. It remains as a compatibility wrapper
// for callers that do not consume the message settings.
func Load(path string) (model.RuleSet, error) {
	file, err := LoadFile(path)
	if err != nil {
		return model.RuleSet{}, err
	}
	return file.RuleSet, nil
}

// Parse compiles only the rules from data. It remains as a compatibility wrapper
// for callers that do not consume the message settings.
func Parse(data []byte) (model.RuleSet, error) {
	file, err := ParseFile(data)
	if err != nil {
		return model.RuleSet{}, err
	}
	return file.RuleSet, nil
}
