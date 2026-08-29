package updater

import (
	"errors"
	"fmt"
	"strings"
	"text/template"
	textparse "text/template/parse"
	"unicode"
	"unicode/utf8"
)

const (
	// DefaultPullRequestTitleTemplate is used when config.yaml omits messages.pullRequestTitle.
	DefaultPullRequestTitleTemplate = "[{{.Environment}}][Image Updater][{{.ImageName}}] Update image"
	// DefaultPullRequestBodyTemplate is used when config.yaml omits messages.pullRequestBody.
	DefaultPullRequestBodyTemplate = "{{.DefaultBody}}"
	// DefaultCommitMessageTemplate is used when config.yaml omits messages.commitMessage.
	DefaultCommitMessageTemplate = "[{{.Environment}}][image-committer][{{.ImageRepository}}] Update image"

	maxMessageTemplateBytes  = 64 << 10
	maxRenderedMessageBytes  = 1 << 20
	maxPullRequestTitleRunes = 256
	maxCommitMessageRunes    = 4 << 10
	maxPullRequestBodyRunes  = 64 << 10
)

var (
	errRenderedMessageTooLarge = errors.New("rendered message is too large")

	allowedMessageTemplateFields = map[string]struct{}{
		"Environment":     {},
		"ImageName":       {},
		"ImageRepository": {},
		"Image":           {},
		"ImageTag":        {},
		"DefaultBody":     {},
	}
)

// MessageTemplates configures the user-facing text generated for an update.
// Templates use Go text/template syntax and the fields documented in the
// English and Japanese configuration guides.
type MessageTemplates struct {
	PullRequestTitle string
	PullRequestBody  string
	CommitMessage    string
}

// MessageRenderer holds templates parsed once during application startup. A
// parsed text/template is safe for concurrent execution.
type MessageRenderer struct {
	pullRequestTitle *template.Template
	pullRequestBody  *template.Template
	commitMessage    *template.Template
}

type messageTemplateData struct {
	Environment     string
	ImageName       string
	ImageRepository string
	Image           string
	ImageTag        string
	DefaultBody     string
}

type renderedMessages struct {
	pullRequestTitle string
	pullRequestBody  string
	commitMessage    string
}

// NewMessageRenderer parses and validates all configured templates.
func NewMessageRenderer(templates MessageTemplates) (*MessageRenderer, error) {
	pullRequestTitle, err := parseMessageTemplate("pull request title", templates.PullRequestTitle)
	if err != nil {
		return nil, err
	}
	pullRequestBody, err := parseMessageTemplate("pull request body", templates.PullRequestBody)
	if err != nil {
		return nil, err
	}
	commitMessage, err := parseMessageTemplate("commit message", templates.CommitMessage)
	if err != nil {
		return nil, err
	}

	renderer := &MessageRenderer{
		pullRequestTitle: pullRequestTitle,
		pullRequestBody:  pullRequestBody,
		commitMessage:    commitMessage,
	}

	// Execute representative data at startup in addition to validating every
	// syntax-tree branch. This catches invalid output constraints before the
	// first registry event arrives.
	if _, err := renderer.render(messageTemplateData{
		Environment:     "production",
		ImageName:       "platform/example",
		ImageRepository: "apps/platform/example",
		Image:           "registry.example.com/apps/platform/example",
		ImageTag:        "abcdef1",
		DefaultBody:     "## Image Update\n",
	}); err != nil {
		return nil, fmt.Errorf("validate message templates: %w", err)
	}

	return renderer, nil
}

func parseMessageTemplate(name, value string) (*template.Template, error) {
	if strings.TrimSpace(value) == "" {
		return nil, fmt.Errorf("%s template is empty", name)
	}
	if len(value) > maxMessageTemplateBytes {
		return nil, fmt.Errorf("%s template exceeds %d bytes", name, maxMessageTemplateBytes)
	}

	// A literal \n is also accepted for compact single-line configuration, while
	// YAML block scalars are preferred for multiline pull request bodies.
	value = strings.ReplaceAll(value, `\n`, "\n")

	parsed, err := template.New(name).Option("missingkey=error").Parse(value)
	if err != nil {
		return nil, fmt.Errorf("parse %s template: %w", name, err)
	}
	if err := validateMessageTemplate(parsed); err != nil {
		return nil, fmt.Errorf("validate %s template: %w", name, err)
	}
	return parsed, nil
}

func validateMessageTemplate(tmpl *template.Template) error {
	for _, associated := range tmpl.Templates() {
		if associated.Tree == nil || associated.Root == nil {
			continue
		}
		if err := validateMessageTemplateNode(associated.Root); err != nil {
			return err
		}
	}
	return nil
}

func validateMessageTemplateNode(node textparse.Node) error {
	switch current := node.(type) {
	case *textparse.ListNode:
		if current == nil {
			return nil
		}
		for _, child := range current.Nodes {
			if err := validateMessageTemplateNode(child); err != nil {
				return err
			}
		}
	case *textparse.ActionNode:
		return validateMessageTemplateNode(current.Pipe)
	case *textparse.IfNode:
		return errors.New("if actions are not allowed")
	case *textparse.WithNode:
		return errors.New("with actions are not allowed")
	case *textparse.RangeNode:
		return errors.New("range actions are not allowed")
	case *textparse.TemplateNode:
		return errors.New("template invocations are not allowed")
	case *textparse.PipeNode:
		if len(current.Decl) > 0 || current.IsAssign {
			return errors.New("template variables are not allowed")
		}
		for _, command := range current.Cmds {
			if err := validateMessageTemplateNode(command); err != nil {
				return err
			}
		}
	case *textparse.CommandNode:
		for _, argument := range current.Args {
			if err := validateMessageTemplateNode(argument); err != nil {
				return err
			}
		}
	case *textparse.FieldNode:
		return validateMessageTemplateField(current.Ident)
	case *textparse.ChainNode:
		if err := validateMessageTemplateNode(current.Node); err != nil {
			return err
		}
		return validateMessageTemplateField(current.Field)
	case *textparse.IdentifierNode:
		return fmt.Errorf("function %q is not allowed", current.Ident)
	case *textparse.VariableNode:
		return errors.New("template variables are not allowed")
	case *textparse.TextNode, *textparse.DotNode, *textparse.NilNode,
		*textparse.BoolNode, *textparse.NumberNode, *textparse.StringNode,
		*textparse.CommentNode:
		return nil
	default:
		return fmt.Errorf("template node %T is not allowed", node)
	}
	return nil
}

func validateMessageTemplateField(fields []string) error {
	if len(fields) != 1 {
		return fmt.Errorf("nested field %q is not allowed", strings.Join(fields, "."))
	}
	if _, allowed := allowedMessageTemplateFields[fields[0]]; !allowed {
		return fmt.Errorf("field %q is not allowed", fields[0])
	}
	return nil
}

func (r *MessageRenderer) render(data messageTemplateData) (renderedMessages, error) {
	title, err := executeMessageTemplate(r.pullRequestTitle, data)
	if err != nil {
		return renderedMessages{}, fmt.Errorf("render pull request title: %w", err)
	}
	if err := validateSingleLine("pull request title", title, maxPullRequestTitleRunes); err != nil {
		return renderedMessages{}, err
	}

	commit, err := executeMessageTemplate(r.commitMessage, data)
	if err != nil {
		return renderedMessages{}, fmt.Errorf("render commit message: %w", err)
	}
	if err := validateSingleLine("commit message", commit, maxCommitMessageRunes); err != nil {
		return renderedMessages{}, err
	}

	bodyData := data
	bodyData.Environment = escapeMarkdown(bodyData.Environment)
	bodyData.ImageName = escapeMarkdown(bodyData.ImageName)
	bodyData.ImageRepository = escapeMarkdown(bodyData.ImageRepository)
	bodyData.Image = escapeMarkdown(bodyData.Image)
	bodyData.ImageTag = escapeMarkdown(bodyData.ImageTag)

	body, err := executeMessageTemplate(r.pullRequestBody, bodyData)
	if err != nil {
		return renderedMessages{}, fmt.Errorf("render pull request body: %w", err)
	}
	if err := validateBody(body); err != nil {
		return renderedMessages{}, err
	}

	return renderedMessages{
		pullRequestTitle: title,
		pullRequestBody:  body,
		commitMessage:    commit,
	}, nil
}

type limitedMessageWriter struct {
	builder strings.Builder
	limit   int
}

func (w *limitedMessageWriter) Write(data []byte) (int, error) {
	if len(data) > w.limit-w.builder.Len() {
		return 0, errRenderedMessageTooLarge
	}
	return w.builder.Write(data)
}

func (w *limitedMessageWriter) String() string { return w.builder.String() }

func executeMessageTemplate(tmpl *template.Template, data messageTemplateData) (string, error) {
	output := limitedMessageWriter{limit: maxRenderedMessageBytes}
	if err := tmpl.Execute(&output, data); err != nil {
		return "", err
	}
	return output.String(), nil
}

func validateSingleLine(name, value string, maxRunes int) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("rendered %s is empty", name)
	}
	if utf8.RuneCountInString(value) > maxRunes {
		return fmt.Errorf("rendered %s exceeds %d characters", name, maxRunes)
	}
	for _, char := range value {
		if unicode.IsControl(char) || char == '\u2028' || char == '\u2029' {
			return fmt.Errorf("rendered %s contains a line or control character", name)
		}
	}
	return nil
}

func validateBody(value string) error {
	if strings.TrimSpace(value) == "" {
		return errors.New("rendered pull request body is empty")
	}
	if utf8.RuneCountInString(value) > maxPullRequestBodyRunes {
		return fmt.Errorf("rendered pull request body exceeds %d characters", maxPullRequestBodyRunes)
	}
	for _, char := range value {
		if (unicode.IsControl(char) && char != '\n' && char != '\r' && char != '\t') ||
			char == '\u2028' || char == '\u2029' {
			return errors.New("rendered pull request body contains an unsupported control character")
		}
	}
	return nil
}
