package commanddiag

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// RewriteParseError maps parser failures for a single bare word token to the
// user-facing unknown-command diagnostic expected in command entry UIs.
func RewriteParseError(script string, err error) error {
	if err == nil {
		return nil
	}
	token, ok := unknownCommandToken(script)
	if !ok {
		return err
	}
	return fmt.Errorf("unknown command `%s'", token)
}

// PendingScript returns the first newline-terminated command from a pending
// command buffer, or the full buffer when no newline has been entered yet.
func PendingScript(pending []rune) string {
	for i, r := range pending {
		if r == '\n' {
			return string(pending[:i+1])
		}
	}
	return string(pending)
}

func unknownCommandToken(script string) (string, bool) {
	token := strings.TrimSpace(script)
	if utf8.RuneCountInString(token) <= 1 {
		return "", false
	}
	if strings.HasPrefix(token, ":") {
		for _, r := range token {
			if r == ':' || r == '-' || r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) {
				continue
			}
			return "", false
		}
		return token, true
	}
	for _, r := range token {
		if unicode.IsSpace(r) {
			return "", false
		}
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '-' {
			return "", false
		}
	}
	return token, true
}
