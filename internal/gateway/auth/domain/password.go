package domain

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	MaxLoginUsernameRunes = 80
	MinOwnerPasswordBytes = 12
	MaxOwnerPasswordBytes = 1024
)

// NormalizeLoginUsername is shared by Gateway password setup/login and the
// private operator CLI. It deliberately returns the same sanitized verdict
// for each invalid form; callers may label the field without echoing it.
func NormalizeLoginUsername(raw string) (string, error) {
	if !utf8.ValidString(raw) {
		return "", ErrInvalidRequest
	}
	value := strings.TrimSpace(raw)
	if value == "" || len([]rune(value)) > MaxLoginUsernameRunes {
		return "", ErrInvalidRequest
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return "", ErrInvalidRequest
		}
	}
	return value, nil
}

// ValidateOwnerPassword applies the byte and UTF-8 bounds before any hash or
// RPC. It never formats the supplied bytes into an error.
func ValidateOwnerPassword(password string) error {
	if !utf8.ValidString(password) || len([]byte(password)) < MinOwnerPasswordBytes || len([]byte(password)) > MaxOwnerPasswordBytes {
		return ErrInvalidRequest
	}
	return nil
}
