// Package pagination implements opaque keyset cursors for list endpoints:
// ?limit=<1..200>&cursor=<opaque> → { "items": [...], "next_cursor": "..." | null }.
package pagination

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/opshub/opshub/internal/apperr"
)

const (
	DefaultLimit = 50
	MaxLimit     = 200
)

// Page is the standard list response envelope.
type Page[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

// Params are the parsed paging parameters; Cursor is decoded into the caller's key type.
type Params struct {
	Limit  int
	cursor string
}

// Parse reads limit and cursor from the query string.
func Parse(r *http.Request) (Params, error) {
	p := Params{Limit: DefaultLimit, cursor: r.URL.Query().Get("cursor")}
	if s := r.URL.Query().Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > MaxLimit {
			return Params{}, apperr.Validation([]apperr.FieldError{{Field: "limit", Rule: "range", Param: "1-" + strconv.Itoa(MaxLimit)}})
		}
		p.Limit = n
	}
	return p, nil
}

// FetchSize is the LIMIT to query: one extra row tells Build whether a next page exists.
func (p Params) FetchSize() int32 {
	return int32(p.Limit + 1) // #nosec G115 -- Limit is validated to 1..MaxLimit
}

// Decode unmarshals the cursor into key. It returns false when no cursor was given.
func (p Params) Decode(key any) (bool, error) {
	if p.cursor == "" {
		return false, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(p.cursor)
	if err != nil || json.Unmarshal(raw, key) != nil {
		return false, apperr.Validation([]apperr.FieldError{{Field: "cursor", Rule: "cursor"}})
	}
	return true, nil
}

// Build returns a page from rows fetched with LIMIT limit+1: when the extra row exists, it
// is dropped and a cursor for the last returned row is produced with keyOf.
func Build[R, T any](rows []R, limit int, convert func(R) T, keyOf func(R) any) Page[T] {
	page := Page[T]{Items: make([]T, 0, min(len(rows), limit))}
	for i, r := range rows {
		if i == limit {
			raw, _ := json.Marshal(keyOf(rows[limit-1]))
			c := base64.RawURLEncoding.EncodeToString(raw)
			page.NextCursor = &c
			break
		}
		page.Items = append(page.Items, convert(r))
	}
	return page
}
