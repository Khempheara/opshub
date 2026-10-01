package auth

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/auth/sso"
	"github.com/opshub/opshub/internal/authn"
	"github.com/opshub/opshub/internal/config"
	"github.com/opshub/opshub/internal/crypto"
	"github.com/opshub/opshub/internal/jobs"
	"github.com/opshub/opshub/internal/pagination"
	"github.com/opshub/opshub/internal/store"
	"github.com/opshub/opshub/internal/testutil/pgtest"
)

// fakeJobs records enqueued emails instead of writing River jobs.
type fakeJobs struct {
	mu     sync.Mutex
	emails []jobs.SendEmailArgs
}

func (f *fakeJobs) InsertTx(_ context.Context, _ pgx.Tx, args river.JobArgs, _ *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e, ok := args.(jobs.SendEmailArgs); ok {
		f.emails = append(f.emails, e)
	}
	return &rivertype.JobInsertResult{Job: &rivertype.JobRow{}}, nil
}

// last returns the most recent email of a template sent to addr.
func (f *fakeJobs) last(addr, template string) (jobs.SendEmailArgs, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.emails) - 1; i >= 0; i-- {
		if f.emails[i].To == addr && f.emails[i].Template == template {
			return f.emails[i], true
		}
	}
	return jobs.SendEmailArgs{}, false
}

// tokenFrom extracts the one-time token from an email link (".../path#token=...").
func tokenFrom(t *testing.T, e jobs.SendEmailArgs) string {
	t.Helper()
	link, _ := e.Data["URL"].(string)
	_, frag, ok := strings.Cut(link, "#token=")
	require.True(t, ok, "link has a token fragment: %s", link)
	tok, err := url.QueryUnescape(frag)
	require.NoError(t, err)
	return tok
}

type env struct {
	svc   *Service
	jobs  *fakeJobs
	clock *time.Time
	keys  *crypto.KeyRing
}

var testArgon = authn.Argon2Params{MemoryKiB: 1024, Iterations: 1, Parallelism: 1, SaltLen: 16, KeyLen: 32}

func newEnv(t *testing.T, mutate ...func(*Config)) *env {
	t.Helper()
	pool := pgtest.Pool(t)
	keys, err := crypto.NewKeyRing([]config.NamedKey{{ID: "m1", Key: bytes.Repeat([]byte{3}, 32)}})
	require.NoError(t, err)
	now := time.Now()
	clock := &now
	signer, err := authn.NewJWTSigner([]config.NamedKey{{ID: "k1", Key: bytes.Repeat([]byte{4}, 32)}}, func() time.Time { return *clock })
	require.NoError(t, err)
	cfg := Config{PublicURL: "https://ops.example.com", AllowSignup: true, DefaultLocale: "en", DefaultTimezone: "Asia/Phnom_Penh"}
	for _, m := range mutate {
		m(&cfg)
	}
	fj := &fakeJobs{}
	svc := NewService(pool, cfg, authn.NewHasher(testArgon), signer, keys, fj, slog.New(slog.NewTextHandler(io.Discard, nil)))
	svc.now = func() time.Time { return *clock }
	return &env{svc: svc, jobs: fj, clock: clock, keys: keys}
}

const goodPassword = "mekong-sunrise-tuktuk"

func uniqueEmail(prefix string) string {
	return prefix + "-" + strings.ReplaceAll(uuid.NewString()[:8], "-", "") + "@example.com"
}

func codeOf(t *testing.T, err error) apperr.Code {
	t.Helper()
	require.Error(t, err)
	ae, ok := apperr.From(err)
	require.True(t, ok, "expected apperr, got %v", err)
	return ae.Code
}

// registerVerified creates a verified account and returns its email.
func (e *env) registerVerified(t *testing.T, locale string) string {
	t.Helper()
	email := uniqueEmail("user")
	require.NoError(t, e.svc.Register(context.Background(), RegisterInput{Email: email, Password: goodPassword, DisplayName: "Dara", Locale: locale}))
	msg, ok := e.jobs.last(email, "verify_email")
	require.True(t, ok)
	require.NoError(t, e.svc.VerifyEmail(context.Background(), tokenFrom(t, msg)))
	return email
}

func (e *env) login(t *testing.T, email string) *SessionTokens {
	t.Helper()
	res, err := e.svc.Login(context.Background(), email, goodPassword)
	require.NoError(t, err)
	require.NotNil(t, res.Session)
	return res.Session
}

func asSession(s *SessionTokens) context.Context {
	return authn.WithPrincipal(context.Background(), authn.Principal{Kind: authn.KindSession, UserID: s.User.ID, SessionID: s.SessionID})
}

func TestRegisterVerifyLogin(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	email := uniqueEmail("Dara")

	require.NoError(t, e.svc.Register(ctx, RegisterInput{Email: "  " + strings.ToUpper(email) + " ", Password: goodPassword, DisplayName: "Dara", Locale: "km"}))
	msg, ok := e.jobs.last(strings.ToLower(email), "verify_email")
	require.True(t, ok, "verification email queued")
	assert.Equal(t, "km", msg.Locale, "email in the user's language")
	assert.Contains(t, msg.Data["URL"], "https://ops.example.com/verify-email#token=")

	_, err := e.svc.Login(ctx, email, goodPassword)
	assert.Equal(t, apperr.CodeEmailNotVerified, codeOf(t, err))

	token := tokenFrom(t, msg)
	require.NoError(t, e.svc.VerifyEmail(ctx, token))
	assert.Equal(t, apperr.CodeInvalidToken, codeOf(t, e.svc.VerifyEmail(ctx, token)), "single use")

	sess := e.login(t, email)
	assert.True(t, sess.User.EmailVerified)
	assert.Equal(t, "km", sess.User.Locale)
	assert.False(t, sess.User.IsPlatformAdmin)
	uid, sid, err := e.svc.jwt.Parse(sess.AccessToken)
	require.NoError(t, err)
	assert.Equal(t, sess.User.ID, uid)
	assert.Equal(t, sess.SessionID, sid)
}

func TestRegisterDoesNotRevealExistingAccounts(t *testing.T) {
	e := newEnv(t)
	email := e.registerVerified(t, "en")
	err := e.svc.Register(context.Background(), RegisterInput{Email: email, Password: "another-long-passphrase", DisplayName: "Impostor"})
	require.NoError(t, err, "same response as a new registration")
	_, ok := e.jobs.last(email, "account_exists")
	assert.True(t, ok, "owner is told someone tried to register")
	e.login(t, email) // original password still works
}

func TestRegisterPolicies(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	err := e.svc.Register(ctx, RegisterInput{Email: uniqueEmail("weak"), Password: "password1234", DisplayName: "W"})
	assert.Equal(t, apperr.CodePasswordBreached, codeOf(t, err))

	boot := uniqueEmail("boss")
	closed := newEnv(t, func(c *Config) { c.AllowSignup = false; c.BootstrapAdminEmail = boot })
	err = closed.svc.Register(ctx, RegisterInput{Email: uniqueEmail("x"), Password: goodPassword, DisplayName: "X"})
	assert.Equal(t, apperr.CodeSignupDisabled, codeOf(t, err))

	require.NoError(t, closed.svc.Register(ctx, RegisterInput{Email: boot, Password: goodPassword, DisplayName: "Boss"}))
	msg, _ := closed.jobs.last(boot, "verify_email")
	require.NoError(t, closed.svc.VerifyEmail(ctx, tokenFrom(t, msg)))
	assert.True(t, closed.login(t, boot).User.IsPlatformAdmin, "bootstrap admin becomes platform admin")
}

// TestRegisterWithInvitation: when sign-up is disabled, an open invitation for the same
// address still lets the invitee register; other addresses and dead invitations don't.
func TestRegisterWithInvitation(t *testing.T) {
	closed := newEnv(t, func(c *Config) { c.AllowSignup = false })
	ctx := context.Background()
	q := store.New(closed.svc.pool)
	o, err := q.CreateOrganization(ctx, store.CreateOrganizationParams{Slug: "inv-" + uuid.NewString()[:8], Name: "Invite Co"})
	require.NoError(t, err)
	invite := func(email string, expires time.Time) string {
		token := uuid.NewString()
		_, err := q.CreateInvitation(ctx, store.CreateInvitationParams{
			OrganizationID: o.ID, Email: email, Role: store.MemberRoleDeveloper,
			TokenHash: crypto.HashToken(token), ExpiresAt: expires,
		})
		require.NoError(t, err)
		return token
	}
	invited, expired := uniqueEmail("invited"), uniqueEmail("expired")
	token := invite(invited, time.Now().Add(time.Hour))
	old := invite(expired, time.Now().Add(-time.Hour))

	for _, c := range []struct{ email, token string }{
		{uniqueEmail("other"), token}, // someone else's invitation
		{invited, "not-a-token"},
		{invited, ""},
		{expired, old},
	} {
		err = closed.svc.Register(ctx, RegisterInput{Email: c.email, Password: goodPassword, DisplayName: "X", InvitationToken: c.token})
		assert.Equal(t, apperr.CodeSignupDisabled, codeOf(t, err), c.email)
	}

	require.NoError(t, closed.svc.Register(ctx, RegisterInput{
		Email: strings.ToUpper(invited), Password: goodPassword, DisplayName: "Invitee", InvitationToken: token,
	}))
	_, ok := closed.jobs.last(invited, "verify_email")
	assert.True(t, ok, "the invitee still has to verify the address")
}

func TestLoginLockout(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	email := e.registerVerified(t, "en")

	_, err := e.svc.Login(ctx, uniqueEmail("ghost"), goodPassword)
	assert.Equal(t, apperr.CodeInvalidCredentials, codeOf(t, err), "unknown email looks like a bad password")

	for i := 1; i < LockoutThreshold; i++ {
		_, err := e.svc.Login(ctx, email, "wrong-password-guess")
		assert.Equal(t, apperr.CodeInvalidCredentials, codeOf(t, err))
	}
	_, err = e.svc.Login(ctx, email, "wrong-password-guess")
	assert.Equal(t, apperr.CodeAccountLocked, codeOf(t, err), "5th failure locks")
	_, err = e.svc.Login(ctx, email, goodPassword)
	assert.Equal(t, apperr.CodeAccountLocked, codeOf(t, err), "correct password rejected while locked")

	*e.clock = e.clock.Add(LockoutBase + time.Second)
	e.login(t, email)

	// A success resets the level, so the next lock is 15 minutes again…
	for range LockoutThreshold {
		_, _ = e.svc.Login(ctx, email, "wrong-password-guess")
	}
	*e.clock = e.clock.Add(LockoutBase + time.Second)
	// …but failing again straight after an expired lock doubles it (level 2 → 30 min).
	for range LockoutThreshold {
		_, _ = e.svc.Login(ctx, email, "wrong-password-guess")
	}
	*e.clock = e.clock.Add(LockoutBase + time.Second)
	_, err = e.svc.Login(ctx, email, goodPassword)
	assert.Equal(t, apperr.CodeAccountLocked, codeOf(t, err), "still locked after 15 min at level 2")
	*e.clock = e.clock.Add(LockoutBase)
	e.login(t, email)
}

func TestLockoutDurationDoubles(t *testing.T) {
	assert.Equal(t, 15*time.Minute, lockoutFor(1))
	assert.Equal(t, 30*time.Minute, lockoutFor(2))
	assert.Equal(t, 60*time.Minute, lockoutFor(3))
	assert.Equal(t, LockoutMax, lockoutFor(20))
}

func TestRefreshRotationAndReuseDetection(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	sess := e.login(t, e.registerVerified(t, "en"))

	next, err := e.svc.Refresh(ctx, sess.RefreshToken)
	require.NoError(t, err)
	assert.NotEqual(t, sess.RefreshToken, next.RefreshToken)
	assert.Equal(t, sess.SessionID, next.SessionID)

	_, err = e.svc.Refresh(ctx, sess.RefreshToken)
	assert.Equal(t, apperr.CodeRefreshReused, codeOf(t, err), "replaying a rotated token")
	_, err = e.svc.Refresh(ctx, next.RefreshToken)
	assert.Equal(t, apperr.CodeRefreshInvalid, codeOf(t, err), "the whole session was revoked")

	_, err = e.svc.Refresh(ctx, "")
	assert.Equal(t, apperr.CodeRefreshInvalid, codeOf(t, err))
	_, err = e.svc.Refresh(ctx, "unknown")
	assert.Equal(t, apperr.CodeRefreshInvalid, codeOf(t, err))
}

func TestRefreshExpiryAndLogout(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	email := e.registerVerified(t, "en")

	sess := e.login(t, email)
	*e.clock = e.clock.Add(RefreshTokenTTL + time.Minute)
	_, err := e.svc.Refresh(ctx, sess.RefreshToken)
	assert.Equal(t, apperr.CodeRefreshInvalid, codeOf(t, err), "idle for longer than the refresh TTL")

	sess = e.login(t, email)
	require.NoError(t, e.svc.Logout(ctx, sess.RefreshToken))
	_, err = e.svc.Refresh(ctx, sess.RefreshToken)
	assert.Equal(t, apperr.CodeRefreshInvalid, codeOf(t, err))
	require.NoError(t, e.svc.Logout(ctx, "unknown"), "logout is idempotent")
}

func TestPasswordReset(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	email := e.registerVerified(t, "km")
	old := e.login(t, email)

	require.NoError(t, e.svc.ForgotPassword(ctx, uniqueEmail("nobody")), "no enumeration")
	require.NoError(t, e.svc.ForgotPassword(ctx, email))
	msg, ok := e.jobs.last(email, "reset_password")
	require.True(t, ok)
	assert.Equal(t, "km", msg.Locale)
	token := tokenFrom(t, msg)

	assert.Equal(t, apperr.CodePasswordTooShort, codeOf(t, e.svc.ResetPassword(ctx, token, "short")))
	require.NoError(t, e.svc.ResetPassword(ctx, token, "a-brand-new-passphrase"), "token still valid after a policy failure")
	assert.Equal(t, apperr.CodeInvalidToken, codeOf(t, e.svc.ResetPassword(ctx, token, "yet-another-passphrase")))

	_, err := e.svc.Refresh(ctx, old.RefreshToken)
	assert.Equal(t, apperr.CodeRefreshInvalid, codeOf(t, err), "reset signs out every session")
	_, err = e.svc.Login(ctx, email, "a-brand-new-passphrase")
	require.NoError(t, err)
	_, ok = e.jobs.last(email, "password_changed")
	assert.True(t, ok, "security notice sent")

	*e.clock = e.clock.Add(2 * time.Hour)
	require.NoError(t, e.svc.ForgotPassword(ctx, email))
	msg, _ = e.jobs.last(email, "reset_password")
	*e.clock = e.clock.Add(PasswordResetTTL + time.Minute)
	// Expiry is checked by the database clock; move the token into the past instead.
	_, err = pgtest.Pool(t).Exec(ctx, "UPDATE email_tokens SET expires_at = now() - interval '1 minute' WHERE token_hash = $1", crypto.HashToken(tokenFrom(t, msg)))
	require.NoError(t, err)
	assert.Equal(t, apperr.CodeInvalidToken, codeOf(t, e.svc.ResetPassword(ctx, tokenFrom(t, msg), "expired-token-passphrase")))
}

func (e *env) totpCode(t *testing.T, secret string) string {
	t.Helper()
	c, err := authn.TOTPCode(secret, *e.clock)
	require.NoError(t, err)
	return c
}

func TestTwoFactorLifecycle(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	email := e.registerVerified(t, "en")
	sess := e.login(t, email)
	me := asSession(sess)

	_, err := e.svc.EnableTOTP(me, "123456")
	assert.Equal(t, apperr.CodeMFASetupRequired, codeOf(t, err))
	_, err = e.svc.SetupTOTP(me, "wrong-password-guess")
	assert.Equal(t, apperr.CodePasswordIncorrect, codeOf(t, err))

	setup, err := e.svc.SetupTOTP(me, goodPassword)
	require.NoError(t, err)
	assert.Contains(t, setup.OTPAuthURI, "otpauth://totp/OpsHub:")
	_, err = e.svc.EnableTOTP(me, "000000")
	assert.Equal(t, apperr.CodeMFAInvalidCode, codeOf(t, err))

	codes, err := e.svc.EnableTOTP(me, e.totpCode(t, setup.Secret))
	require.NoError(t, err)
	assert.Len(t, codes, RecoveryCodeCount)
	_, ok := e.jobs.last(email, "two_factor_enabled")
	assert.True(t, ok)

	// Password login now requires the second factor.
	*e.clock = e.clock.Add(time.Minute)
	res, err := e.svc.Login(ctx, email, goodPassword)
	require.NoError(t, err)
	require.Nil(t, res.Session)
	require.NotEmpty(t, res.MFAToken)

	_, err = e.svc.LoginMFA(ctx, res.MFAToken, "000000", "")
	assert.Equal(t, apperr.CodeMFAInvalidCode, codeOf(t, err))
	code := e.totpCode(t, setup.Secret)
	full, err := e.svc.LoginMFA(ctx, res.MFAToken, code, "")
	require.NoError(t, err)
	assert.True(t, full.User.TwoFactorEnabled)
	_, err = e.svc.LoginMFA(ctx, res.MFAToken, code, "")
	assert.Equal(t, apperr.CodeMFAChallengeExpired, codeOf(t, err), "challenge is single use")

	// The same TOTP code cannot be replayed on a new challenge.
	res2, _ := e.svc.Login(ctx, email, goodPassword)
	_, err = e.svc.LoginMFA(ctx, res2.MFAToken, code, "")
	assert.Equal(t, apperr.CodeMFAInvalidCode, codeOf(t, err))

	// Recovery codes work once, in any formatting.
	_, err = e.svc.LoginMFA(ctx, res2.MFAToken, "", strings.ToUpper(strings.ReplaceAll(codes[0], "-", " ")))
	require.NoError(t, err)
	res3, _ := e.svc.Login(ctx, email, goodPassword)
	_, err = e.svc.LoginMFA(ctx, res3.MFAToken, "", codes[0])
	assert.Equal(t, apperr.CodeMFAInvalidCode, codeOf(t, err))
	left, err := e.svc.RecoveryCodesRemaining(me)
	require.NoError(t, err)
	assert.EqualValues(t, RecoveryCodeCount-1, left)

	// Too many wrong codes exhaust the challenge.
	res4, _ := e.svc.Login(ctx, email, goodPassword)
	for range MaxMFAAttempts {
		_, _ = e.svc.LoginMFA(ctx, res4.MFAToken, "000000", "")
	}
	*e.clock = e.clock.Add(time.Minute)
	_, err = e.svc.LoginMFA(ctx, res4.MFAToken, e.totpCode(t, setup.Secret), "")
	assert.Equal(t, apperr.CodeMFAChallengeExpired, codeOf(t, err))

	// Regenerate, then disable.
	*e.clock = e.clock.Add(time.Minute)
	newCodes, err := e.svc.RegenerateRecoveryCodes(me, e.totpCode(t, setup.Secret))
	require.NoError(t, err)
	assert.NotEqual(t, codes, newCodes)
	*e.clock = e.clock.Add(time.Minute)
	require.NoError(t, e.svc.DisableTOTP(me, goodPassword, e.totpCode(t, setup.Secret), ""))
	res5, err := e.svc.Login(ctx, email, goodPassword)
	require.NoError(t, err)
	assert.NotNil(t, res5.Session, "no second factor after disabling")
}

func TestTOTPSecretIsEncryptedAtRest(t *testing.T) {
	e := newEnv(t)
	sess := e.login(t, e.registerVerified(t, "en"))
	setup, err := e.svc.SetupTOTP(asSession(sess), goodPassword)
	require.NoError(t, err)
	var raw []byte
	require.NoError(t, pgtest.Pool(t).QueryRow(context.Background(), "SELECT totp_pending_enc FROM users WHERE id = $1", sess.User.ID).Scan(&raw))
	assert.NotContains(t, string(raw), setup.Secret)
	pt, err := e.keys.Decrypt(raw, totpAAD(sess.User.ID))
	require.NoError(t, err)
	assert.Equal(t, setup.Secret, string(pt))
}

func TestProfileOptimisticLocking(t *testing.T) {
	e := newEnv(t)
	sess := e.login(t, e.registerVerified(t, "en"))
	me := asSession(sess)

	km, tz, yes := "km", "UTC", true
	u, err := e.svc.UpdateProfile(me, sess.User.Version, UpdateProfileInput{Locale: &km, Timezone: &tz, KhmerNumerals: &yes})
	require.NoError(t, err)
	assert.Equal(t, "km", u.Locale)
	assert.Equal(t, "UTC", u.Timezone)
	assert.True(t, u.KhmerNumerals)
	assert.Equal(t, "Dara", u.DisplayName, "omitted fields are kept")
	assert.Equal(t, sess.User.Version+1, u.Version)

	name := "Stale Write"
	_, err = e.svc.UpdateProfile(me, sess.User.Version, UpdateProfileInput{DisplayName: &name})
	assert.Equal(t, apperr.CodeVersionConflict, codeOf(t, err))

	got, err := e.svc.Me(me)
	require.NoError(t, err)
	assert.Equal(t, u, got)
}

func TestChangePasswordKeepsCurrentSession(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	email := e.registerVerified(t, "en")
	other := e.login(t, email)
	current := e.login(t, email)
	me := asSession(current)

	assert.Equal(t, apperr.CodePasswordIncorrect, codeOf(t, e.svc.ChangePassword(me, "wrong-password-guess", "another-long-passphrase")))
	require.NoError(t, e.svc.ChangePassword(me, goodPassword, "another-long-passphrase"))

	_, err := e.svc.Refresh(ctx, other.RefreshToken)
	assert.Equal(t, apperr.CodeRefreshInvalid, codeOf(t, err), "other devices signed out")
	_, err = e.svc.Refresh(ctx, current.RefreshToken)
	assert.NoError(t, err, "the device that changed the password stays signed in")
}

func TestSessionsListAndRevoke(t *testing.T) {
	e := newEnv(t)
	email := e.registerVerified(t, "en")
	a := e.login(t, email)
	b := e.login(t, email)
	me := asSession(b)

	page, err := e.svc.ListSessions(me, pagination.Params{Limit: 1})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.True(t, page.Items[0].Current, "newest first; b is the caller")
	require.NotNil(t, page.NextCursor)

	require.NoError(t, e.svc.RevokeSession(me, a.SessionID))
	assert.Equal(t, apperr.CodeSessionNotFound, codeOf(t, e.svc.RevokeSession(me, a.SessionID)))

	// Another user cannot revoke b's session.
	intruder := e.login(t, e.registerVerified(t, "en"))
	assert.Equal(t, apperr.CodeSessionNotFound, codeOf(t, e.svc.RevokeSession(asSession(intruder), b.SessionID)))
}

func TestAPITokens(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	sess := e.login(t, e.registerVerified(t, "en"))
	me := asSession(sess)

	days := 30
	created, err := e.svc.CreateAPIToken(me, CreateAPITokenInput{Name: "CI", Scopes: []string{"api:write", "api:read", "api:read"}, ExpiresInDays: &days})
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(created.Token, created.Prefix+"_"))
	assert.Equal(t, []string{"api:read", "api:write"}, created.Scopes)

	uid, tid, scopes, err := e.svc.LookupAPIToken(ctx, crypto.HashToken(created.Token))
	require.NoError(t, err)
	assert.Equal(t, sess.User.ID, uid)
	assert.Equal(t, created.ID, tid)
	assert.Equal(t, created.Scopes, scopes)

	page, err := e.svc.ListAPITokens(me, pagination.Params{Limit: 10})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, "CI", page.Items[0].Name)

	require.NoError(t, e.svc.RevokeAPIToken(me, created.ID))
	_, _, _, err = e.svc.LookupAPIToken(ctx, crypto.HashToken(created.Token))
	assert.Error(t, err, "revoked tokens stop working")
	assert.Equal(t, apperr.CodeTokenNotFound, codeOf(t, e.svc.RevokeAPIToken(me, created.ID)))
}

func TestSSOLogin(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	email := uniqueEmail("sso")
	res, err := e.svc.LoginWithIdentity(ctx, sso.Identity{Provider: "google", Subject: "g-" + email, Email: email, EmailVerified: true, Name: "Srey Pov"})
	require.NoError(t, err)
	require.NotNil(t, res.Session)
	assert.True(t, res.Session.User.EmailVerified)
	assert.False(t, res.Session.User.HasPassword)
	assert.Equal(t, "Srey Pov", res.Session.User.DisplayName)

	again, err := e.svc.LoginWithIdentity(ctx, sso.Identity{Provider: "google", Subject: "g-" + email, Email: "changed@example.com", EmailVerified: true})
	require.NoError(t, err)
	assert.Equal(t, res.Session.User.ID, again.Session.User.ID, "linked identity wins over the email claim")

	_, err = e.svc.LoginWithIdentity(ctx, sso.Identity{Provider: "github", Subject: "gh-" + email, Email: uniqueEmail("x"), EmailVerified: false})
	assert.Equal(t, apperr.CodeSSOEmailUnverified, codeOf(t, err))

	// A verified email links to an existing password account.
	pwEmail := e.registerVerified(t, "en")
	linked, err := e.svc.LoginWithIdentity(ctx, sso.Identity{Provider: "github", Subject: "gh-" + pwEmail, Email: pwEmail, EmailVerified: true})
	require.NoError(t, err)
	assert.True(t, linked.Session.User.HasPassword)

	// The only login method of an SSO-only account cannot be unlinked.
	me := asSession(res.Session)
	ids, err := e.svc.ListIdentities(me)
	require.NoError(t, err)
	require.Len(t, ids, 1)
	assert.Equal(t, apperr.CodeLastLoginMethod, codeOf(t, e.svc.UnlinkIdentity(me, ids[0].ID)))
	ids2, _ := e.svc.ListIdentities(asSession(linked.Session))
	require.NoError(t, e.svc.UnlinkIdentity(asSession(linked.Session), ids2[0].ID), "password accounts can unlink")

	closed := newEnv(t, func(c *Config) { c.AllowSignup = false })
	_, err = closed.svc.LoginWithIdentity(ctx, sso.Identity{Provider: "google", Subject: "new", Email: uniqueEmail("n"), EmailVerified: true})
	assert.Equal(t, apperr.CodeSignupDisabled, codeOf(t, err))
}

func TestAuditTrail(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	email := e.registerVerified(t, "en")
	e.login(t, email)
	_, _ = e.svc.Login(ctx, email, "wrong-password-guess")

	u, err := store.New(pgtest.Pool(t)).GetUserByEmail(ctx, email)
	require.NoError(t, err)
	rows, err := pgtest.Pool(t).Query(ctx, "SELECT action FROM audit_log WHERE actor_user_id = $1 ORDER BY created_at, id", u.ID)
	require.NoError(t, err)
	actions, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)
	assert.Equal(t, []string{"user.register", "user.email_verified", "auth.login", "auth.login_failed"}, actions)
}

func TestVerifyEmailByOperator(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	pool := pgtest.Pool(t)
	email := uniqueEmail("first-admin")
	require.NoError(t, e.svc.Register(ctx, RegisterInput{Email: email, Password: goodPassword, DisplayName: "Sokha", Locale: "en"}))
	_, err := e.svc.Login(ctx, email, goodPassword)
	assert.Equal(t, apperr.CodeEmailNotVerified, codeOf(t, err))

	already, err := VerifyEmailByOperator(ctx, pool, "  "+strings.ToUpper(email))
	require.NoError(t, err)
	assert.False(t, already)
	e.login(t, email)
	var actorType, action string
	require.NoError(t, pool.QueryRow(ctx, `SELECT actor_type, action FROM audit_log WHERE resource_id = (SELECT id::text FROM users WHERE email = $1)
		AND action = 'user.verify_operator'`, email).Scan(&actorType, &action))
	assert.Equal(t, "system", actorType)

	already, err = VerifyEmailByOperator(ctx, pool, email)
	require.NoError(t, err)
	assert.True(t, already, "a second time changes nothing")
	_, err = VerifyEmailByOperator(ctx, pool, uniqueEmail("nobody"))
	assert.ErrorIs(t, err, ErrNoSuchAccount)
}
