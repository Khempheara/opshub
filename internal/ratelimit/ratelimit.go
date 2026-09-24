// Package ratelimit implements per-key token-bucket rate limiting for HTTP handlers.
//
// Limits are kept in process memory, so with N API replicas the effective limit is up to
// N× the configured value. That is acceptable for abuse protection; security-critical
// counters (login failures / account lockout) are persisted in PostgreSQL instead.
package ratelimit

import (
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/httpx"
)

// Limiter holds one token bucket per key.
type Limiter struct {
	rate  rate.Limit
	burst int
	ttl   time.Duration
	now   func() time.Time

	mu        sync.Mutex
	buckets   map[string]*bucket
	lastSweep time.Time
}

type bucket struct {
	lim      *rate.Limiter
	lastSeen time.Time
}

// New allows `perSecond` events per second with bursts of `burst` per key.
func New(perSecond float64, burst int) *Limiter {
	return &Limiter{
		rate: rate.Limit(perSecond), burst: burst, ttl: 10 * time.Minute, now: time.Now,
		buckets: map[string]*bucket{},
	}
}

// PerMinute is a convenience for low-rate limits such as login attempts.
func PerMinute(n int) *Limiter { return New(float64(n)/60, n) }

// Result describes the outcome of Allow.
type Result struct {
	Allowed    bool
	Limit      int
	Remaining  int
	RetryAfter time.Duration
}

// Allow consumes one token for key.
func (l *Limiter) Allow(key string) Result {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweep(now)
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{lim: rate.NewLimiter(l.rate, l.burst)}
		l.buckets[key] = b
	}
	b.lastSeen = now
	res := b.lim.ReserveN(now, 1)
	if delay := res.DelayFrom(now); delay > 0 {
		res.CancelAt(now)
		return Result{Allowed: false, Limit: l.burst, RetryAfter: delay}
	}
	return Result{Allowed: true, Limit: l.burst, Remaining: int(math.Max(0, math.Floor(b.lim.TokensAt(now))))}
}

// sweep drops idle buckets so memory stays bounded. Caller holds mu.
func (l *Limiter) sweep(now time.Time) {
	if now.Sub(l.lastSweep) < time.Minute {
		return
	}
	l.lastSweep = now
	for k, b := range l.buckets {
		if now.Sub(b.lastSeen) > l.ttl {
			delete(l.buckets, k)
		}
	}
}

// KeyFunc derives the rate-limit key for a request ("" skips limiting).
type KeyFunc func(r *http.Request) string

// Middleware enforces l, emitting RateLimit-* headers and 429 RATE_LIMITED with Retry-After.
func Middleware(l *Limiter, key KeyFunc) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			k := key(r)
			if k == "" {
				next.ServeHTTP(w, r)
				return
			}
			res := l.Allow(k)
			w.Header().Set("RateLimit-Limit", strconv.Itoa(res.Limit))
			w.Header().Set("RateLimit-Remaining", strconv.Itoa(res.Remaining))
			if !res.Allowed {
				secs := int(math.Ceil(res.RetryAfter.Seconds()))
				w.Header().Set("Retry-After", strconv.Itoa(secs))
				w.Header().Set("RateLimit-Reset", strconv.Itoa(secs))
				httpx.Error(w, r, apperr.New(apperr.CodeRateLimited, http.StatusTooManyRequests, "too many requests").
					WithDetails(map[string]any{"retry_after_seconds": secs}))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
