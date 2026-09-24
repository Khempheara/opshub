package authn

import (
	_ "embed"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/opshub/opshub/internal/apperr"
)

const (
	MinPasswordLength = 12
	MaxPasswordLength = 128
)

//go:embed common_passwords.txt
var commonPasswordsRaw string

var commonPasswords = func() map[string]struct{} {
	m := make(map[string]struct{}, 1500)
	for line := range strings.Lines(commonPasswordsRaw) {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			m[line] = struct{}{}
		}
	}
	return m
}()

// CheckPasswordPolicy enforces the password policy (NIST SP 800-63B style): length
// 12–128 characters, not a known common/breached password, and not derived from the
// account email. No composition rules: length beats complexity.
func CheckPasswordPolicy(password, email string) *apperr.Error {
	n := utf8.RuneCountInString(password)
	if n < MinPasswordLength {
		return apperr.New(apperr.CodePasswordTooShort, http.StatusUnprocessableEntity, "password is too short").
			WithDetails(map[string]any{"min": MinPasswordLength})
	}
	if n > MaxPasswordLength {
		return apperr.New(apperr.CodePasswordTooLong, http.StatusUnprocessableEntity, "password is too long").
			WithDetails(map[string]any{"max": MaxPasswordLength})
	}
	lower := strings.ToLower(password)
	if _, found := commonPasswords[lower]; found {
		return apperr.New(apperr.CodePasswordBreached, http.StatusUnprocessableEntity, "password appears in a list of commonly used passwords")
	}
	if local, _, _ := strings.Cut(strings.ToLower(email), "@"); len(local) >= 4 && strings.Contains(lower, local) {
		return apperr.New(apperr.CodePasswordWeak, http.StatusUnprocessableEntity, "password must not contain your email address")
	}
	if isSingleRepeatedChar(lower) {
		return apperr.New(apperr.CodePasswordWeak, http.StatusUnprocessableEntity, "password is too predictable")
	}
	return nil
}

func isSingleRepeatedChar(s string) bool {
	first, _ := utf8.DecodeRuneInString(s)
	for _, r := range s {
		if r != first {
			return false
		}
	}
	return true
}
