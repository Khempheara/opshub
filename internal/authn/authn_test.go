package authn

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/config"
	"github.com/opshub/opshub/internal/crypto"
)

// Cheap parameters keep tests fast; production uses DefaultArgon2Params.
var testParams = Argon2Params{MemoryKiB: 1024, Iterations: 1, Parallelism: 1, SaltLen: 16, KeyLen: 32}

func TestPasswordHashVerify(t *testing.T) {
	h := NewHasher(testParams)
	enc, err := h.Hash("correct horse battery staple")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(enc, "$argon2id$v=19$m=1024,t=1,p=1$"))

	ok, rehash, err := h.Verify("correct horse battery staple", enc)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.False(t, rehash)

	ok, _, err = h.Verify("wrong", enc)
	require.NoError(t, err)
	assert.False(t, ok)

	enc2, _ := h.Hash("correct horse battery staple")
	assert.NotEqual(t, enc, enc2, "salt must be random")
}

func TestPasswordNeedsRehashWhenParamsIncrease(t *testing.T) {
	enc, _ := NewHasher(testParams).Hash("correct horse battery staple")
	stronger := NewHasher(Argon2Params{MemoryKiB: 2048, Iterations: 2, Parallelism: 1, SaltLen: 16, KeyLen: 32})
	ok, rehash, err := stronger.Verify("correct horse battery staple", enc)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.True(t, rehash)
}

func TestPasswordMalformedHash(t *testing.T) {
	h := NewHasher(testParams)
	for _, bad := range []string{"", "plain", "$argon2i$v=19$m=1,t=1,p=1$c2FsdA$aGFzaA", "$argon2id$v=18$m=1,t=1,p=1$c2FsdA$aGFzaGhhc2hoYXNoaGFzaA", "$argon2id$v=19$m=x$a$b"} {
		_, _, err := h.Verify("x", bad)
		assert.Error(t, err, bad)
	}
	h.VerifyDummy("anything") // must not panic
}

func TestPasswordPolicy(t *testing.T) {
	tests := []struct {
		pw, email string
		want      apperr.Code
	}{
		{"short", "a@b.co", apperr.CodePasswordTooShort},
		{strings.Repeat("x", 129) + "y", "a@b.co", apperr.CodePasswordTooLong},
		{"PASSWORD1234", "a@b.co", apperr.CodePasswordBreached},
		{"dara.sok-2026!!", "dara.sok@example.com", apperr.CodePasswordWeak},
		{"aaaaaaaaaaaaaa", "a@b.co", apperr.CodePasswordWeak},
		{"ដំណើរការ-រហ័ស-២០២៦", "a@b.co", ""}, // Khmer passphrases are fine (length counts runes)
		{"tuk-tuk riding to Kep", "dara@example.com", ""},
	}
	for _, tt := range tests {
		err := CheckPasswordPolicy(tt.pw, tt.email)
		if tt.want == "" {
			assert.Nil(t, err, tt.pw)
			continue
		}
		require.NotNil(t, err, tt.pw)
		assert.Equal(t, tt.want, err.Code, tt.pw)
	}
}

func newSigner(t *testing.T, now func() time.Time, keys ...config.NamedKey) *JWTSigner {
	t.Helper()
	if len(keys) == 0 {
		keys = []config.NamedKey{{ID: "k1", Key: bytes.Repeat([]byte{7}, 32)}}
	}
	s, err := NewJWTSigner(keys, now)
	require.NoError(t, err)
	return s
}

func TestJWTRoundTripAndExpiry(t *testing.T) {
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	s := newSigner(t, clock)
	user, session := uuid.New(), uuid.New()

	tok, exp, err := s.Issue(user, session)
	require.NoError(t, err)
	assert.Equal(t, now.Add(15*time.Minute), exp)

	gotUser, gotSession, err := s.Parse(tok)
	require.NoError(t, err)
	assert.Equal(t, user, gotUser)
	assert.Equal(t, session, gotSession)

	now = now.Add(16 * time.Minute)
	_, _, err = s.Parse(tok)
	assert.ErrorIs(t, err, ErrInvalidAccessToken)
}

func TestJWTRejectsForgeries(t *testing.T) {
	s := newSigner(t, nil)
	tok, _, _ := s.Issue(uuid.New(), uuid.New())

	other := newSigner(t, nil, config.NamedKey{ID: "k1", Key: bytes.Repeat([]byte{9}, 32)})
	_, _, err := other.Parse(tok)
	assert.ErrorIs(t, err, ErrInvalidAccessToken, "signature from a different key")

	// Algorithm confusion: HS256 "signed" with the public key must be rejected.
	pub := s.public["k1"]
	forged := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": uuid.NewString(), "sid": uuid.NewString(), "iss": "opshub", "aud": "opshub-api",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	forged.Header["kid"] = "k1"
	forgedStr, err := forged.SignedString([]byte(pub))
	require.NoError(t, err)
	_, _, err = s.Parse(forgedStr)
	assert.ErrorIs(t, err, ErrInvalidAccessToken)

	// alg=none
	none := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.MapClaims{"sub": uuid.NewString()})
	noneStr, _ := none.SignedString(jwt.UnsafeAllowNoneSignatureType)
	_, _, err = s.Parse(noneStr)
	assert.ErrorIs(t, err, ErrInvalidAccessToken)

	_, _, err = s.Parse("garbage")
	assert.ErrorIs(t, err, ErrInvalidAccessToken)
}

func TestJWTKeyRotation(t *testing.T) {
	k1 := config.NamedKey{ID: "k1", Key: bytes.Repeat([]byte{1}, 32)}
	k2 := config.NamedKey{ID: "k2", Key: bytes.Repeat([]byte{2}, 32)}
	old := newSigner(t, nil, k1)
	tok, _, _ := old.Issue(uuid.New(), uuid.New())

	rotated := newSigner(t, nil, k2, k1)
	_, _, err := rotated.Parse(tok)
	require.NoError(t, err, "tokens signed by the previous key stay valid")

	newTok, _, _ := rotated.Issue(uuid.New(), uuid.New())
	_, _, err = old.Parse(newTok)
	assert.Error(t, err, "old signer doesn't know k2")
	assert.Len(t, rotated.public["k2"], ed25519.PublicKeySize)
}

func TestTOTPRFC6238Vector(t *testing.T) {
	// RFC 6238 Appendix B, SHA1 secret "12345678901234567890"; 8-digit 94287082 at T=59 → 6 digits 287082.
	secret := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	code, err := TOTPCode(secret, time.Unix(59, 0))
	require.NoError(t, err)
	assert.Equal(t, "287082", code)

	step, ok := ValidateTOTP(secret, "287 082", time.Unix(59, 0))
	assert.True(t, ok)
	assert.Equal(t, int64(1), step)

	_, ok = ValidateTOTP(secret, "287082", time.Unix(59+90, 0))
	assert.False(t, ok, "outside the ±1 step window")
	_, ok = ValidateTOTP(secret, "12345", time.Unix(59, 0))
	assert.False(t, ok)
}

func TestTOTPSecretAndURI(t *testing.T) {
	s := NewTOTPSecret()
	assert.Len(t, s, 32)
	uri := TOTPURI(s, "dara@example.com", "OpsHub")
	assert.True(t, strings.HasPrefix(uri, "otpauth://totp/OpsHub:dara@example.com?"))
	assert.Contains(t, uri, "secret="+s)
	code, _ := TOTPCode(s, time.Now())
	_, ok := ValidateTOTP(s, code, time.Now())
	assert.True(t, ok)
}

type fakeTokens struct {
	hash   []byte
	scopes []string
}

func (f fakeTokens) LookupAPIToken(_ context.Context, hash []byte) (uuid.UUID, uuid.UUID, []string, error) {
	if !bytes.Equal(hash, f.hash) {
		return uuid.Nil, uuid.Nil, nil, errors.New("not found")
	}
	return uuid.MustParse("00000000-0000-7000-8000-000000000001"), uuid.New(), f.scopes, nil
}

func TestAuthenticatorMiddleware(t *testing.T) {
	signer := newSigner(t, nil)
	readToken := "ohp_abcd_readsecret"
	a := &Authenticator{JWT: signer, Tokens: fakeTokens{hash: crypto.HashToken(readToken), scopes: []string{ScopeRead}}}

	var got Principal
	var authed bool
	h := a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, authed = PrincipalFrom(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))
	call := func(method, auth string) int {
		req := httptest.NewRequest(method, "/", nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		authed = false
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	assert.Equal(t, http.StatusNoContent, call(http.MethodGet, ""))
	assert.False(t, authed, "anonymous passes through without a principal")

	jwtTok, _, _ := signer.Issue(uuid.New(), uuid.New())
	assert.Equal(t, http.StatusNoContent, call(http.MethodPost, "Bearer "+jwtTok))
	assert.True(t, got.IsSession())

	assert.Equal(t, http.StatusUnauthorized, call(http.MethodGet, "Bearer not-a-jwt"))
	assert.Equal(t, http.StatusUnauthorized, call(http.MethodGet, "Basic abc"))
	assert.Equal(t, http.StatusUnauthorized, call(http.MethodGet, "Bearer ohp_unknown"))

	assert.Equal(t, http.StatusNoContent, call(http.MethodGet, "Bearer "+readToken))
	assert.Equal(t, KindAPIToken, got.Kind)
	assert.Equal(t, http.StatusForbidden, call(http.MethodPost, "Bearer "+readToken), "api:read cannot write")

	// Runner and job tokens are left to the runner API: anonymous to everyone else.
	for _, tok := range []string{"ohr_abc_secret", "ohr_reg_secret", "ohj_secret", "ohi_abc_secret"} {
		assert.Equal(t, http.StatusNoContent, call(http.MethodPost, "Bearer "+tok))
		assert.False(t, authed, tok)
	}
}

func TestRequireSessionAndAuth(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	run := func(h http.Handler, p *Principal) int {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		if p != nil {
			req = req.WithContext(WithPrincipal(req.Context(), *p))
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	session := &Principal{Kind: KindSession, UserID: uuid.New()}
	token := &Principal{Kind: KindAPIToken, UserID: uuid.New(), Scopes: []string{ScopeWrite}}

	assert.Equal(t, http.StatusUnauthorized, run(RequireAuth(ok), nil))
	assert.Equal(t, http.StatusNoContent, run(RequireAuth(ok), token))
	assert.Equal(t, http.StatusUnauthorized, run(RequireSession(ok), nil))
	assert.Equal(t, http.StatusForbidden, run(RequireSession(ok), token))
	assert.Equal(t, http.StatusNoContent, run(RequireSession(ok), session))
}

func TestCSRF(t *testing.T) {
	h := CSRF([]string{"https://ops.example.com"})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	call := func(cookie, header, origin, site string) int {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", nil)
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: CSRFCookie, Value: cookie})
		}
		if header != "" {
			req.Header.Set(CSRFHeader, header)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if site != "" {
			req.Header.Set("Sec-Fetch-Site", site)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	assert.Equal(t, http.StatusNoContent, call("tok", "tok", "https://ops.example.com", "same-origin"))
	assert.Equal(t, http.StatusNoContent, call("tok", "tok", "", ""))
	assert.Equal(t, http.StatusForbidden, call("tok", "", "", ""), "header missing")
	assert.Equal(t, http.StatusForbidden, call("", "tok", "", ""), "cookie missing")
	assert.Equal(t, http.StatusForbidden, call("tok", "other", "", ""), "mismatch")
	assert.Equal(t, http.StatusForbidden, call("tok", "tok", "https://evil.example", ""), "foreign origin")
	assert.Equal(t, http.StatusForbidden, call("tok", "tok", "", "cross-site"))
}
