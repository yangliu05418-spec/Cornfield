package auth

import (
	"errors"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@-]{2,63}$`)

func NormalizeUsername(value string) string {
	return strings.TrimSpace(value)
}

func ValidateUsername(value string) error {
	if !utf8.ValidString(value) || !usernamePattern.MatchString(value) {
		return errors.New("username must be 3 to 64 ASCII letters, digits, or ._@-")
	}
	return nil
}

func NormalizeDisplayName(value string) string {
	return strings.TrimSpace(value)
}

func ValidateDisplayName(value string) error {
	if !utf8.ValidString(value) {
		return errors.New("display name must be valid UTF-8")
	}
	characters := utf8.RuneCountInString(value)
	if characters < 1 || characters > 128 {
		return errors.New("display name must be between 1 and 128 characters")
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return errors.New("display name must not contain control characters")
		}
	}
	return nil
}
