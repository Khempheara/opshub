// Package i18n provides server-side localization for text the backend renders itself
// (notifications, email templates). API errors are NOT localized here: they carry codes
// that the frontend translates.
//
// Locale priority: user profile setting → Accept-Language → "en".
package i18n

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strings"

	goi18n "github.com/nicksnyder/go-i18n/v2/i18n"
	"golang.org/x/text/language"
)

//go:embed locales/*.json
var localeFS embed.FS

const (
	English = "en"
	Khmer   = "km"
	// Fallback is used when no preference matches.
	Fallback = English
)

// supported is ordered: the first entry is the matcher's default.
var supported = []language.Tag{language.English, language.Khmer}

var matcher = language.NewMatcher(supported)

// Bundle holds the loaded translation messages.
type Bundle struct {
	b *goi18n.Bundle
}

// NewBundle loads the embedded locales/*.json files.
func NewBundle() (*Bundle, error) {
	b := goi18n.NewBundle(language.English)
	b.RegisterUnmarshalFunc("json", json.Unmarshal)
	entries, err := fs.ReadDir(localeFS, "locales")
	if err != nil {
		return nil, fmt.Errorf("read locales: %w", err)
	}
	for _, e := range entries {
		if _, err := b.LoadMessageFileFS(localeFS, path.Join("locales", e.Name())); err != nil {
			return nil, fmt.Errorf("load %s: %w", e.Name(), err)
		}
	}
	return &Bundle{b: b}, nil
}

// T translates id into locale, falling back to English for missing messages.
func (b *Bundle) T(locale, id string, data map[string]any) string {
	loc := goi18n.NewLocalizer(b.b, locale, Fallback)
	msg, err := loc.Localize(&goi18n.LocalizeConfig{MessageID: id, TemplateData: data})
	if err != nil {
		return id
	}
	return msg
}

// Lookup translates id into locale (English fallback) and reports whether the message
// exists in any bundle.
func (b *Bundle) Lookup(locale, id string, data map[string]any) (string, bool) {
	loc := goi18n.NewLocalizer(b.b, locale, Fallback)
	msg, err := loc.Localize(&goi18n.LocalizeConfig{MessageID: id, TemplateData: data})
	if err != nil {
		return "", false
	}
	return msg, true
}

// Normalize maps any BCP-47 tag (e.g. "km-KH") to a supported locale, or "" if none.
func Normalize(tag string) string {
	t, err := language.Parse(strings.TrimSpace(tag))
	if err != nil {
		return ""
	}
	_, idx, conf := matcher.Match(t)
	if conf == language.No {
		return ""
	}
	return baseOf(supported[idx])
}

// Negotiate picks the locale from the user's saved preference, then Accept-Language.
func Negotiate(userPref, acceptLanguage string) string {
	if l := Normalize(userPref); l != "" {
		return l
	}
	tags, _, err := language.ParseAcceptLanguage(acceptLanguage)
	if err != nil || len(tags) == 0 {
		return Fallback
	}
	_, idx, conf := matcher.Match(tags...)
	if conf == language.No {
		return Fallback
	}
	return baseOf(supported[idx])
}

func baseOf(t language.Tag) string {
	b, _ := t.Base()
	return b.String()
}

type ctxKey struct{}

// WithLocale stores the request locale in ctx.
func WithLocale(ctx context.Context, locale string) context.Context {
	return context.WithValue(ctx, ctxKey{}, locale)
}

// LocaleFrom returns the request locale, or Fallback.
func LocaleFrom(ctx context.Context) string {
	if l, ok := ctx.Value(ctxKey{}).(string); ok && l != "" {
		return l
	}
	return Fallback
}

// Middleware negotiates the request locale from Accept-Language. The auth middleware
// overrides it with the user's profile locale once authenticated.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		loc := Negotiate("", r.Header.Get("Accept-Language"))
		w.Header().Set("Content-Language", loc)
		w.Header().Add("Vary", "Accept-Language")
		next.ServeHTTP(w, r.WithContext(WithLocale(r.Context(), loc)))
	})
}
