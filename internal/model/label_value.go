package model

import (
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Label values are chosen by whoever pushed the image, which is a much lower bar
// than being allowed to approve a pull request in the manifest repository. Left
// as they arrive, a value can open new lines in the pull request description or
// in the managed comment block of a manifest, and take those documents over.
//
// Every value is therefore normalised here, at the one place labels enter the
// domain, so no consumer has to remember to do it. Normalising is structural
// only: it does not escape anything for a particular output format, because the
// same value is rendered both as Markdown and as YAML.
//
// A value that cannot be normalised into something usable is dropped rather than
// mangled. Labels decorate a pull request; losing one costs context, keeping a
// hostile one costs trust in the review.

const (
	// maxLabelTextLength caps a free-form value. A label is a caption, not a
	// payload.
	maxLabelTextLength = 512

	// maxLabelURLLength caps a value used as a link.
	maxLabelURLLength = 2048

	// maxLabelHandleLength is the longest GitHub login.
	maxLabelHandleLength = 39

	// maxLabelTokenLength caps a git ref, a revision or a timestamp.
	maxLabelTokenLength = 256

	// tokenExtraCharacters are the non alphanumeric characters a git ref, a
	// revision or an RFC 3339 timestamp is allowed to carry.
	tokenExtraCharacters = "._-/:+"
)

// sanitizeLabelText normalises a free-form label value.
//
// Line breaks, control characters and other non printable runes are removed, so
// the value can never start a new line of Markdown or of a YAML comment block.
// Runs of whitespace collapse into a single space.
//
// Returns:
//
//	The normalised value, truncated to maxLabelTextLength, or an empty string
//	when nothing printable is left.
func sanitizeLabelText(value string) string {
	var builder strings.Builder
	builder.Grow(len(value))

	pendingSpace := false
	for _, char := range value {
		switch {
		case char == utf8.RuneError, !unicode.IsPrint(char):
			// Line breaks and control characters land here and are dropped, but
			// they still separate words, so remember the gap.
			pendingSpace = builder.Len() > 0
		case unicode.IsSpace(char):
			pendingSpace = builder.Len() > 0
		default:
			if pendingSpace {
				builder.WriteByte(' ')
				pendingSpace = false
			}
			builder.WriteRune(char)
		}
	}

	return truncateRunes(builder.String(), maxLabelTextLength)
}

// sanitizeLabelURL keeps a value only if it is a link that is safe to publish.
//
// Anything that is not an absolute http or https URL is dropped, which rules out
// javascript:, data: and relative values. Embedded credentials are dropped too,
// so a link cannot smuggle a secret or spoof an origin.
//
// Unlike a free-form value this one is never repaired. Truncating a URL or
// replacing a stray character in it would produce a different link that still
// looks plausible, which is worse than having no link at all.
//
// Returns:
//
//	The URL, or an empty string when it is not usable.
func sanitizeLabelURL(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxLabelURLLength {
		return ""
	}

	// A URL never legitimately carries whitespace or a control character.
	if strings.ContainsFunc(value, func(char rune) bool {
		return unicode.IsSpace(char) || !unicode.IsPrint(char)
	}) {
		return ""
	}

	parsed, err := url.Parse(value)
	if err != nil {
		return ""
	}

	switch {
	case parsed.Scheme != "http" && parsed.Scheme != "https":
		return ""
	case parsed.Host == "":
		return ""
	case parsed.User != nil:
		return ""
	}

	return parsed.String()
}

// sanitizeLabelHandle keeps a value only if it looks like a GitHub login.
//
// The value is handed to the API as an assignee and reviewer and rendered as an
// @mention, so a free-form value would let a label ping arbitrary teams.
//
// Returns:
//
//	The handle, or an empty string when the value is not one.
func sanitizeLabelHandle(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxLabelHandleLength {
		return ""
	}

	// A login is alphanumeric with single inner hyphens.
	previousHyphen := false
	for i, char := range value {
		switch {
		case isASCIIAlphanumeric(char):
			previousHyphen = false
		case char == '-' && i > 0 && !previousHyphen:
			previousHyphen = true
		default:
			return ""
		}
	}

	if previousHyphen {
		return ""
	}

	return value
}

// sanitizeLabelNumber keeps a value only if it is a plain decimal number.
//
// The value is interpolated into a URL path, so anything else could forge a link.
//
// Returns:
//
//	The number, or an empty string when the value is not one.
func sanitizeLabelNumber(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxLabelTokenLength {
		return ""
	}

	for _, char := range value {
		if char < '0' || char > '9' {
			return ""
		}
	}

	return value
}

// sanitizeLabelToken keeps a value only if it is made of the characters a git
// ref, a revision or a timestamp can carry.
//
// Returns:
//
//	The token, or an empty string when the value carries anything else.
func sanitizeLabelToken(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxLabelTokenLength {
		return ""
	}

	for _, char := range value {
		if !isASCIIAlphanumeric(char) && !strings.ContainsRune(tokenExtraCharacters, char) {
			return ""
		}
	}

	return value
}

func isASCIIAlphanumeric(char rune) bool {
	return (char >= 'a' && char <= 'z') ||
		(char >= 'A' && char <= 'Z') ||
		(char >= '0' && char <= '9')
}

// truncateRunes cuts a string to at most limit runes, never splitting one.
func truncateRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}

	count := 0
	for index := range value {
		if count == limit {
			return strings.TrimRight(value[:index], " ")
		}
		count++
	}

	return value
}
