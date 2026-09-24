// Package idempotency implements the Idempotency-Key header for POST endpoints that create
// things or trigger work (docs/architecture.md). The first request with a key runs normally
// and its response is stored for 24 hours; a retry with the same key and the same request
// gets the stored response (header Idempotent-Replayed: true) without running the handler
// again. The same key with a different request is 409 IDEMPOTENCY_KEY_REUSED; a retry while
// the first request is still running is 409 IDEMPOTENCY_KEY_IN_PROGRESS.
//
// Keys are scoped to the authenticated user. Responses with a 5xx status are not stored, so
// the client can retry after a server error. Requests without the header are unaffected.
package idempotency

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/authn"
	"github.com/opshub/opshub/internal/database"
	"github.com/opshub/opshub/internal/httpx"
	"github.com/opshub/opshub/internal/store"
)

const (
	Header         = "Idempotency-Key"
	ReplayedHeader = "Idempotent-Replayed"
	TTL            = 24 * time.Hour
	maxKeyLength   = 255
)

// Middleware applies idempotency to the routes it wraps. It must run after authentication.
func Middleware(pool *pgxpool.Pool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := r.Header.Get(Header)
			p, ok := authn.PrincipalFrom(r.Context())
			if key == "" || r.Method != http.MethodPost || !ok {
				next.ServeHTTP(w, r)
				return
			}
			if len(key) > maxKeyLength {
				httpx.Error(w, r, apperr.Validation([]apperr.FieldError{{Field: Header, Rule: "max", Param: "255"}}))
				return
			}
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, httpx.MaxBodyBytes))
			if err != nil {
				var tooLarge *http.MaxBytesError
				if errors.As(err, &tooLarge) {
					httpx.Error(w, r, apperr.New(apperr.CodePayloadTooLarge, http.StatusRequestEntityTooLarge, "request body too large"))
					return
				}
				httpx.Error(w, r, apperr.New(apperr.CodeBadRequest, http.StatusBadRequest, "could not read request body"))
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			hash := requestHash(r, body)

			q := store.New(pool)
			id, err := q.ClaimIdempotencyKey(r.Context(), store.ClaimIdempotencyKeyParams{
				UserID: p.UserID, Key: key, RequestHash: hash, ExpiresAt: time.Now().Add(TTL),
			})
			if database.IsNoRows(err) {
				replay(w, r, q, p, key, hash)
				return
			}
			if err != nil {
				httpx.Error(w, r, err)
				return
			}

			rec := &recorder{ResponseWriter: w, status: http.StatusOK}
			defer func() {
				// Store (or release) the key even if the client went away mid-request.
				ctx := context.WithoutCancel(r.Context())
				if rec.status >= http.StatusInternalServerError {
					_ = q.ReleaseIdempotencyKey(ctx, id)
					return
				}
				status := int32(rec.status) // #nosec G115 -- HTTP status codes fit in int32
				_ = q.CompleteIdempotencyKey(ctx, store.CompleteIdempotencyKeyParams{
					ID: id, ResponseStatus: &status, ResponseBody: rec.body.Bytes(),
				})
			}()
			next.ServeHTTP(rec, r)
		})
	}
}

func replay(w http.ResponseWriter, r *http.Request, q *store.Queries, p authn.Principal, key string, hash []byte) {
	rec, err := q.GetIdempotencyKey(r.Context(), store.GetIdempotencyKeyParams{UserID: p.UserID, Key: key})
	if database.IsNoRows(err) {
		// Released by a failed first attempt between our claim and this read: ask to retry.
		httpx.Error(w, r, inProgress())
		return
	}
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if !bytes.Equal(rec.RequestHash, hash) {
		httpx.Error(w, r, apperr.New(apperr.CodeIdempotencyKeyReused, http.StatusConflict,
			"this Idempotency-Key was already used for a different request"))
		return
	}
	if rec.ResponseStatus == nil {
		httpx.Error(w, r, inProgress())
		return
	}
	w.Header().Set(ReplayedHeader, "true")
	if len(rec.ResponseBody) > 0 {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
	}
	w.WriteHeader(int(*rec.ResponseStatus))
	_, _ = w.Write(rec.ResponseBody)
}

func inProgress() *apperr.Error {
	return apperr.New(apperr.CodeIdempotencyKeyInProgress, http.StatusConflict,
		"a request with this Idempotency-Key is still being processed; retry shortly")
}

// requestHash identifies the request a key was first used for: method, path and body.
func requestHash(r *http.Request, body []byte) []byte {
	h := sha256.New()
	h.Write([]byte(r.Method))
	h.Write([]byte{0})
	h.Write([]byte(r.URL.Path))
	h.Write([]byte{0})
	h.Write(body)
	return h.Sum(nil)
}

// recorder passes the response through while keeping a copy of the status and body.
type recorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
	body        bytes.Buffer
}

func (rw *recorder) WriteHeader(status int) {
	if !rw.wroteHeader {
		rw.status, rw.wroteHeader = status, true
	}
	rw.ResponseWriter.WriteHeader(status)
}

func (rw *recorder) Write(b []byte) (int, error) {
	if !rw.wroteHeader {
		rw.WriteHeader(http.StatusOK)
	}
	rw.body.Write(b)
	return rw.ResponseWriter.Write(b)
}
