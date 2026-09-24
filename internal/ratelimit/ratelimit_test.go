package ratelimit

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestAllowAndRefill(t *testing.T) {
	now := time.Unix(0, 0)
	l := New(1, 2)
	l.now = func() time.Time { return now }

	assert.True(t, l.Allow("a").Allowed)
	assert.True(t, l.Allow("a").Allowed)
	denied := l.Allow("a")
	assert.False(t, denied.Allowed)
	assert.InDelta(t, time.Second, denied.RetryAfter, float64(10*time.Millisecond))
	assert.True(t, l.Allow("b").Allowed, "keys are independent")

	now = now.Add(time.Second)
	assert.True(t, l.Allow("a").Allowed, "a token refills after 1s")
}

func TestSweepDropsIdleBuckets(t *testing.T) {
	now := time.Unix(0, 0)
	l := New(1, 1)
	l.now = func() time.Time { return now }
	l.Allow("idle")
	now = now.Add(11 * time.Minute)
	l.Allow("fresh")
	assert.NotContains(t, l.buckets, "idle")
}

func TestMiddleware(t *testing.T) {
	l := PerMinute(1)
	h := Middleware(l, func(r *http.Request) string { return r.Header.Get("X-Key") })(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	call := func(key string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.Header.Set("X-Key", key)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	assert.Equal(t, http.StatusNoContent, call("k").Code)
	rec := call("k")
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Equal(t, "60", rec.Header().Get("Retry-After"))
	assert.Contains(t, rec.Body.String(), `"RATE_LIMITED"`)
	assert.Equal(t, http.StatusNoContent, call("").Code, "empty key is not limited")
}
