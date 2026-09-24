package idempotency

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/opshub/opshub/internal/authn"
	"github.com/opshub/opshub/internal/store"
	"github.com/opshub/opshub/internal/testutil/pgtest"
)

func setup(t *testing.T, handler http.HandlerFunc) (http.Handler, uuid.UUID) {
	t.Helper()
	pool := pgtest.Pool(t)
	return Middleware(pool)(handler), newUser(t, pool)
}

func newUser(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	u, err := store.New(pool).CreateUser(context.Background(), store.CreateUserParams{
		Email: uuid.NewString() + "@example.com", DisplayName: "Idem", Locale: "en", Timezone: "UTC",
	})
	require.NoError(t, err)
	return u.ID
}

func send(h http.Handler, user uuid.UUID, method, path, key, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if key != "" {
		req.Header.Set(Header, key)
	}
	if user != uuid.Nil {
		req = req.WithContext(authn.WithPrincipal(req.Context(), authn.Principal{Kind: authn.KindSession, UserID: user}))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestReplaysTheFirstResponse(t *testing.T) {
	var calls atomic.Int32
	h, user := setup(t, func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(w, `{"call":%d}`, n)
	})

	first := send(h, user, "POST", "/api/v1/things", "key-1", `{"name":"a"}`)
	assert.Equal(t, http.StatusCreated, first.Code)
	assert.JSONEq(t, `{"call":1}`, first.Body.String())
	assert.Empty(t, first.Header().Get(ReplayedHeader))

	again := send(h, user, "POST", "/api/v1/things", "key-1", `{"name":"a"}`)
	assert.Equal(t, http.StatusCreated, again.Code)
	assert.JSONEq(t, `{"call":1}`, again.Body.String())
	assert.Equal(t, "true", again.Header().Get(ReplayedHeader))
	assert.EqualValues(t, 1, calls.Load(), "the handler ran once")

	reused := send(h, user, "POST", "/api/v1/things", "key-1", `{"name":"b"}`)
	assert.Equal(t, http.StatusConflict, reused.Code)
	assert.Contains(t, reused.Body.String(), "IDEMPOTENCY_KEY_REUSED")
	otherPath := send(h, user, "POST", "/api/v1/other", "key-1", `{"name":"a"}`)
	assert.Contains(t, otherPath.Body.String(), "IDEMPOTENCY_KEY_REUSED")

	// Keys are per user: someone else's identical key is independent.
	other := newUser(t, pgtest.Pool(t))
	fresh := send(h, other, "POST", "/api/v1/things", "key-1", `{"name":"a"}`)
	assert.JSONEq(t, `{"call":2}`, fresh.Body.String())
}

func TestPassThroughWithoutKeyOrPrincipalOrForGET(t *testing.T) {
	var calls atomic.Int32
	h, user := setup(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	})
	send(h, user, "POST", "/x", "", `{}`)
	send(h, user, "POST", "/x", "", `{}`)
	send(h, user, "GET", "/x", "k", "")
	send(h, user, "GET", "/x", "k", "")
	send(h, uuid.Nil, "POST", "/x", "k", `{}`)
	assert.EqualValues(t, 5, calls.Load())
}

func TestServerErrorsAreNotStored(t *testing.T) {
	var calls atomic.Int32
	h, user := setup(t, func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
	})
	assert.Equal(t, http.StatusInternalServerError, send(h, user, "POST", "/x", "k", `{}`).Code)
	assert.Equal(t, http.StatusCreated, send(h, user, "POST", "/x", "k", `{}`).Code, "retry runs again")
	assert.Equal(t, http.StatusCreated, send(h, user, "POST", "/x", "k", `{}`).Code)
	assert.EqualValues(t, 2, calls.Load())
}

func TestConcurrentRetryIsInProgress(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	h, user := setup(t, func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		w.WriteHeader(http.StatusCreated)
	})
	done := make(chan int)
	go func() { done <- send(h, user, "POST", "/x", "slow", `{}`).Code }()
	<-started
	second := send(h, user, "POST", "/x", "slow", `{}`)
	assert.Equal(t, http.StatusConflict, second.Code)
	assert.Contains(t, second.Body.String(), "IDEMPOTENCY_KEY_IN_PROGRESS")
	close(release)
	assert.Equal(t, http.StatusCreated, <-done)
}

func TestKeyTooLong(t *testing.T) {
	h, user := setup(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusCreated) })
	rec := send(h, user, "POST", "/x", strings.Repeat("k", 256), `{}`)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Contains(t, rec.Body.String(), "VALIDATION_FAILED")
}
