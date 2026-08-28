package model

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
)

const (
	// tagRegexpPrefix marks a tag pattern as a regular expression.
	tagRegexpPrefix = "regexp:"
	// variableMarker starts a capture in an image pattern, e.g. $1.
	variableMarker = "$"
	// wildcardSegment matches any single path segment without capturing it.
	wildcardSegment = "*"
	// tagPatternSeparator splits the repository pattern from the tag pattern.
	tagPatternSeparator = ":"
	// tagPieceSeparator splits a tag into the pieces a tag pattern captures.
	tagPieceSeparator = "."
)

// Rule maps images pushed to a registry onto the manifests that have to be
// updated. It is one entry of the configuration file.
type Rule struct {
	// ImagePattern matches the pushed image. The first segment is the registry
	// host, the rest is the repository path where a segment may be:
	//
	//	literal   has to match the pushed segment exactly
	//	*         matches any segment without capturing it
	//	$n        matches any segment and captures it as $n
	//
	// The last segment may carry a ":tagPattern" suffix whose dot separated
	// pieces are captured the same way.
	//
	//	123456789012.dkr.ecr.ap-northeast-1.amazonaws.com/apps/internal/tools/$1:$2.*
	//
	// Capture names are substituted textually, so $1 and $10 must not be mixed
	// in one rule.
	ImagePattern string

	// ManifestURL is where the manifests to update live, with the captures of
	// ImagePattern still to expand.
	//
	//	https://github.com/example-org/example-manifests/services/$2/$1/overlays/development
	ManifestURL string

	// Env labels the environment the manifests belong to. It shows up in the
	// branch name, the commit message and the pull request title.
	Env string

	// AllowTag, when set, is the only tag the rule accepts.
	// "regexp:<expr>" matches by regular expression, anything else is compared
	// literally.
	AllowTag string

	// DenyTags are the tags the rule rejects, same syntax as AllowTag.
	DenyTags []string

	// WriteImageManifest tells whether the well-known image manifest comment
	// block has to be maintained. A nil value means enabled.
	WriteImageManifest *bool
}

// ManifestLocation is a manifest directory inside a repository.
type ManifestLocation struct {
	// RepositoryURL clones the repository,
	// e.g. https://github.com/example-org/example-manifests.
	RepositoryURL string
	// Owner is the account the repository belongs to.
	Owner string
	// Repository is the repository name.
	Repository string
	// Dir is the manifest directory relative to the repository root. It is
	// empty when the manifests sit at the root.
	Dir string
}

// RuleSet is the configured set of rules with their patterns already compiled,
// so a malformed pattern fails at startup instead of on the first event.
type RuleSet struct {
	rules []compiledRule
}

// MatchedRule is the rule that covers a given event. It carries the event so
// the captures of the pattern only have to be resolved once.
type MatchedRule struct {
	compiled compiledRule
	event    ImagePushEvent
}

type compiledRule struct {
	rule      Rule
	host      string
	segments  []string
	tagPieces []string
	allow     tagMatcher
	deny      []tagMatcher
}

// tagMatcher matches a tag against one configured pattern.
type tagMatcher struct {
	literal string
	re      *regexp.Regexp
}

// NewRuleSet compiles rules, rejecting any rule that cannot be used.
//
// Returns:
//
//	The rule set, or an error naming the offending rule.
func NewRuleSet(rules []Rule) (RuleSet, error) {
	compiled := make([]compiledRule, 0, len(rules))

	for i, rule := range rules {
		c, err := compileRule(rule)
		if err != nil {
			return RuleSet{}, fmt.Errorf("rule %d (%s): %w", i, rule.ImagePattern, err)
		}
		compiled = append(compiled, c)
	}

	return RuleSet{rules: compiled}, nil
}

// Len is the number of rules in the set.
func (s RuleSet) Len() int { return len(s.rules) }

// Match finds the rule that covers event.
//
// A rule matches when the registry host is identical and the repository path
// has the same number of segments, with every literal segment equal. The rule
// with the most literal segments wins; the first one declared wins a tie.
//
// Returns:
//
//	The matched rule, or ErrNoMatchingRule when no rule covers the event.
func (s RuleSet) Match(event ImagePushEvent) (MatchedRule, error) {
	best := -1
	bestWeight := 0

	for i, rule := range s.rules {
		weight, ok := rule.match(event)
		if !ok {
			continue
		}
		if best < 0 || weight > bestWeight {
			best, bestWeight = i, weight
		}
	}

	if best < 0 {
		return MatchedRule{}, fmt.Errorf("%w for %s", ErrNoMatchingRule, event.Reference())
	}

	return MatchedRule{compiled: s.rules[best], event: event}, nil
}

// Rule is the configured rule behind the match.
func (m MatchedRule) Rule() Rule { return m.compiled.rule }

// Env labels the environment the manifests belong to.
func (m MatchedRule) Env() string { return m.compiled.rule.Env }

// WritesImageManifest reports whether the well-known image manifest comment
// block has to be maintained for this rule.
func (m MatchedRule) WritesImageManifest() bool {
	return m.compiled.rule.WriteImageManifest == nil || *m.compiled.rule.WriteImageManifest
}

// ValidateTag checks the pushed tag against the allow and deny patterns.
//
// Returns:
//
//	nil when the tag is accepted, ErrImageTagNotAllowed when it does not match
//	AllowTag and ErrImageTagDenied when it matches one of DenyTags.
func (m MatchedRule) ValidateTag() error {
	tag := m.event.Tag

	if !m.compiled.allow.empty() && !m.compiled.allow.matches(tag) {
		return fmt.Errorf("%w: %q does not match %q", ErrImageTagNotAllowed, tag, m.compiled.rule.AllowTag)
	}

	for i, deny := range m.compiled.deny {
		if deny.matches(tag) {
			return fmt.Errorf("%w: %q matches %q", ErrImageTagDenied, tag, m.compiled.rule.DenyTags[i])
		}
	}

	return nil
}

// Location expands the captures of the rule into the manifest location.
//
// Returns:
//
//	The location, or ErrIncompleteRule when the event does not provide every
//	capture the rule needs or the result is not a usable repository URL.
func (m MatchedRule) Location() (ManifestLocation, error) {
	variables, err := m.compiled.variables(m.event)
	if err != nil {
		return ManifestLocation{}, err
	}

	expanded := expandVariables(m.compiled.rule.ManifestURL, variables)
	if strings.Contains(expanded, variableMarker) {
		return ManifestLocation{}, fmt.Errorf("%w: unresolved variables in %q", ErrIncompleteRule, expanded)
	}

	return parseManifestURL(expanded)
}

func compileRule(rule Rule) (compiledRule, error) {
	host, segments, tagPieces := splitImagePattern(rule.ImagePattern)
	if host == "" {
		return compiledRule{}, fmt.Errorf("image pattern has no registry host")
	}
	if len(segments) == 0 {
		return compiledRule{}, fmt.Errorf("image pattern has no repository path")
	}
	if strings.TrimSpace(rule.ManifestURL) == "" {
		return compiledRule{}, fmt.Errorf("manifest URL is empty")
	}

	allow, err := newTagMatcher(rule.AllowTag)
	if err != nil {
		return compiledRule{}, err
	}

	deny := make([]tagMatcher, 0, len(rule.DenyTags))
	for _, pattern := range rule.DenyTags {
		matcher, err := newTagMatcher(pattern)
		if err != nil {
			return compiledRule{}, err
		}
		// An empty deny entry would reject every image whose tag is empty,
		// which is never what a configuration file means.
		if matcher.empty() {
			continue
		}
		deny = append(deny, matcher)
	}

	return compiledRule{
		rule:      rule,
		host:      host,
		segments:  segments,
		tagPieces: tagPieces,
		allow:     allow,
		deny:      deny,
	}, nil
}

// match reports whether the rule covers event and how many literal segments
// it matched, which is the specificity used to pick between rules.
func (c compiledRule) match(event ImagePushEvent) (int, bool) {
	if event.Host == "" || event.Host != c.host {
		return 0, false
	}

	repository := splitPath(event.Repository)
	if len(repository) != len(c.segments) {
		return 0, false
	}

	weight := 0
	for i, segment := range c.segments {
		if segment == wildcardSegment || isVariable(segment) {
			continue
		}
		if segment != repository[i] {
			return 0, false
		}
		weight++
	}

	return weight, true
}

// variables resolves the captures of the pattern from event.
func (c compiledRule) variables(event ImagePushEvent) (map[string]string, error) {
	repository := splitPath(event.Repository)
	if len(repository) != len(c.segments) {
		return nil, fmt.Errorf("%w: %q does not fit %q", ErrIncompleteRule, event.Repository, c.rule.ImagePattern)
	}

	variables := make(map[string]string, len(c.segments)+len(c.tagPieces))
	for i, segment := range c.segments {
		if isVariable(segment) {
			variables[segment] = repository[i]
		}
	}

	if len(c.tagPieces) == 0 {
		return variables, nil
	}

	pieces := strings.Split(event.Tag, tagPieceSeparator)
	for i, piece := range c.tagPieces {
		if !isVariable(piece) {
			continue
		}
		if i >= len(pieces) {
			return nil, fmt.Errorf("%w: tag %q has no piece for %s of %q",
				ErrIncompleteRule, event.Tag, piece, c.rule.ImagePattern)
		}
		variables[piece] = pieces[i]
	}

	return variables, nil
}

func newTagMatcher(pattern string) (tagMatcher, error) {
	pattern = strings.TrimSpace(pattern)

	expr, isRegexp := strings.CutPrefix(pattern, tagRegexpPrefix)
	if !isRegexp {
		return tagMatcher{literal: pattern}, nil
	}

	re, err := regexp.Compile(strings.TrimSpace(expr))
	if err != nil {
		return tagMatcher{}, fmt.Errorf("invalid tag pattern %q: %w", pattern, err)
	}

	return tagMatcher{re: re}, nil
}

func (m tagMatcher) empty() bool { return m.re == nil && m.literal == "" }

func (m tagMatcher) matches(tag string) bool {
	if m.re != nil {
		return m.re.MatchString(tag)
	}
	return m.literal == tag
}

// splitImagePattern breaks an image pattern into its host, its repository
// segments and the dot separated pieces of its optional tag pattern.
func splitImagePattern(pattern string) (host string, segments, tagPieces []string) {
	parts := splitPath(pattern)
	if len(parts) == 0 {
		return "", nil, nil
	}

	host, segments = parts[0], parts[1:]
	if len(segments) == 0 {
		return host, nil, nil
	}

	last := segments[len(segments)-1]
	repository, tagPattern, ok := strings.Cut(last, tagPatternSeparator)
	if !ok {
		return host, segments, nil
	}

	segments[len(segments)-1] = repository
	return host, segments, strings.Split(tagPattern, tagPieceSeparator)
}

// expandVariables substitutes the captures into a template. Longer names go
// first so that $10 is not corrupted by the substitution of $1.
func expandVariables(template string, variables map[string]string) string {
	names := make([]string, 0, len(variables))
	for name := range variables {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool { return len(names[i]) > len(names[j]) })

	for _, name := range names {
		template = strings.ReplaceAll(template, name, variables[name])
	}

	return template
}

func parseManifestURL(raw string) (ManifestLocation, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ManifestLocation{}, fmt.Errorf("%w: %q is not a URL: %v", ErrIncompleteRule, raw, err)
	}

	if parsed.Scheme == "" || parsed.Host == "" {
		return ManifestLocation{}, fmt.Errorf("%w: %q has no scheme or host", ErrIncompleteRule, raw)
	}

	segments := splitPath(parsed.Path)
	if len(segments) < 2 {
		return ManifestLocation{}, fmt.Errorf("%w: %q has no owner and repository", ErrIncompleteRule, raw)
	}

	// The captures come from an external event, so the directory is checked for
	// escaping the working copy before anything joins it to a filesystem path.
	dir := path.Join(segments[2:]...)
	if path.IsAbs(dir) || dir == ".." || strings.HasPrefix(dir, "../") {
		return ManifestLocation{}, fmt.Errorf("%w: %q escapes the repository", ErrIncompleteRule, raw)
	}

	return ManifestLocation{
		RepositoryURL: fmt.Sprintf("%s://%s/%s/%s", parsed.Scheme, parsed.Host, segments[0], segments[1]),
		Owner:         segments[0],
		Repository:    segments[1],
		Dir:           dir,
	}, nil
}

func isVariable(segment string) bool { return strings.Contains(segment, variableMarker) }

// splitPath splits a slash separated path, dropping empty and blank segments so
// that a leading slash or a doubled separator does not shift the indexes.
func splitPath(raw string) []string {
	return slices.DeleteFunc(strings.Split(raw, "/"), func(segment string) bool {
		return strings.TrimSpace(segment) == ""
	})
}
