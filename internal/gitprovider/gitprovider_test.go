package gitprovider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeHost records requests and answers like GitHub or GitLab would.
type fakeHost struct {
	t        *testing.T
	requests []*http.Request
	bodies   []map[string]any
	handle   func(w http.ResponseWriter, r *http.Request)
}

func (f *fakeHost) server() *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.requests = append(f.requests, r)
		f.bodies = append(f.bodies, body)
		f.handle(w, r)
	}))
	f.t.Cleanup(srv.Close)
	return srv
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func TestGitHubClient(t *testing.T) {
	f := &fakeHost{t: t}
	f.handle = func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /repos/acme/api":
			writeJSON(w, 200, map[string]any{
				"id": 42, "full_name": "acme/api", "html_url": "https://github.com/acme/api",
				"clone_url": "https://github.com/acme/api.git", "default_branch": "trunk",
				"permissions": map[string]bool{"admin": true},
			})
		case "POST /repos/acme/api/hooks":
			writeJSON(w, 201, map[string]any{"id": 7})
		case "DELETE /repos/acme/api/hooks/7", "DELETE /repos/acme/api/hooks/8":
			if r.URL.Path == "/repos/acme/api/hooks/8" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case "GET /repos/acme/private":
			w.WriteHeader(http.StatusNotFound)
		case "GET /repos/acme/badtoken":
			w.WriteHeader(http.StatusUnauthorized)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}
	srv := f.server()
	fac := Factory{HTTP: srv.Client(), GitHubAPIURL: srv.URL}
	ctx := context.Background()

	c, err := fac.Client(GitHub, "", "acme/api", "ghp_secret")
	require.NoError(t, err)
	repo, err := c.Repo(ctx)
	require.NoError(t, err)
	assert.Equal(t, Repo{ExternalID: "42", FullName: "acme/api", WebURL: "https://github.com/acme/api",
		CloneURL: "https://github.com/acme/api.git", DefaultBranch: "trunk", CanManageHooks: true}, repo)
	assert.Equal(t, "Bearer ghp_secret", f.requests[0].Header.Get("Authorization"))
	assert.Equal(t, "2022-11-28", f.requests[0].Header.Get("X-GitHub-Api-Version"))

	id, err := c.CreateHook(ctx, "https://ops.example.com/api/v1/webhooks/github/r1", "s3cret")
	require.NoError(t, err)
	assert.Equal(t, "7", id)
	hook := f.bodies[1]
	assert.Equal(t, "web", hook["name"])
	assert.Equal(t, []any{"push", "pull_request"}, hook["events"])
	cfg := hook["config"].(map[string]any)
	assert.Equal(t, "s3cret", cfg["secret"])
	assert.Equal(t, "json", cfg["content_type"])
	assert.Equal(t, "0", cfg["insecure_ssl"])

	require.NoError(t, c.DeleteHook(ctx, "7"))
	require.NoError(t, c.DeleteHook(ctx, "8"), "an already-deleted hook is fine")

	c, _ = fac.Client(GitHub, "", "acme/private", "t")
	_, err = c.Repo(ctx)
	assert.ErrorIs(t, err, ErrNotFound)
	c, _ = fac.Client(GitHub, "", "acme/badtoken", "t")
	_, err = c.Repo(ctx)
	assert.ErrorIs(t, err, ErrUnauthorized)
	c, _ = fac.Client(GitHub, "", "acme/other", "t")
	_, err = c.Repo(ctx)
	assert.ErrorIs(t, err, ErrUnavailable)
}

func TestGitHubEnterpriseAndGitLabSelfHostedURLs(t *testing.T) {
	fac := Factory{}
	c, err := fac.Client(GitHub, "https://ghe.example.com", "a/b", "t")
	require.NoError(t, err)
	assert.Equal(t, "https://ghe.example.com/api/v3", c.(*github).api.base)
	c, err = fac.Client(GitHub, "", "a/b", "t")
	require.NoError(t, err)
	assert.Equal(t, "https://api.github.com", c.(*github).api.base)
	c, err = fac.Client(GitLab, "https://git.example.com/gitlab", "g/sub/p", "t")
	require.NoError(t, err)
	assert.Equal(t, "https://git.example.com/gitlab/api/v4", c.(*gitlab).api.base)
	c, err = fac.Client(GitLab, "", "g/p", "t")
	require.NoError(t, err)
	assert.Equal(t, "https://gitlab.com/api/v4", c.(*gitlab).api.base)
}

func TestGitLabClient(t *testing.T) {
	f := &fakeHost{t: t}
	level := 40
	f.handle = func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.EscapedPath() {
		case "GET /api/v4/projects/grp%2Fsub%2Fapp":
			writeJSON(w, 200, map[string]any{
				"id": 99, "path_with_namespace": "grp/sub/app", "web_url": "https://gitlab.com/grp/sub/app",
				"http_url_to_repo": "https://gitlab.com/grp/sub/app.git", "default_branch": "main",
				"permissions": map[string]any{"project_access": nil, "group_access": map[string]int{"access_level": level}},
			})
		case "POST /api/v4/projects/grp%2Fsub%2Fapp/hooks":
			writeJSON(w, 201, map[string]any{"id": 5})
		case "DELETE /api/v4/projects/grp%2Fsub%2Fapp/hooks/5":
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}
	srv := f.server()
	fac := Factory{HTTP: srv.Client(), GitLabURL: srv.URL}
	ctx := context.Background()

	c, err := fac.Client(GitLab, "", "grp/sub/app", "glpat-x")
	require.NoError(t, err)
	repo, err := c.Repo(ctx)
	require.NoError(t, err)
	assert.Equal(t, "99", repo.ExternalID)
	assert.Equal(t, "https://gitlab.com/grp/sub/app.git", repo.CloneURL)
	assert.True(t, repo.CanManageHooks, "Maintainer via group")
	assert.Equal(t, "glpat-x", f.requests[0].Header.Get("PRIVATE-TOKEN"))

	level = 30 // Developer
	repo, err = c.Repo(ctx)
	require.NoError(t, err)
	assert.False(t, repo.CanManageHooks)

	id, err := c.CreateHook(ctx, "https://ops.example.com/api/v1/webhooks/gitlab/r1", "tok")
	require.NoError(t, err)
	assert.Equal(t, "5", id)
	assert.Equal(t, "tok", f.bodies[2]["token"])
	assert.Equal(t, true, f.bodies[2]["push_events"])
	assert.Equal(t, true, f.bodies[2]["enable_ssl_verification"])
	require.NoError(t, c.DeleteHook(ctx, "5"))
}

func TestInputValidation(t *testing.T) {
	for _, bad := range []string{"", "noslash", "a/b/c", "../etc", "a/..", "a b/c", "a/b?x=1"} {
		assert.False(t, ValidFullName(GitHub, bad), bad)
	}
	assert.True(t, ValidFullName(GitHub, "Khempheara/opshub"))
	assert.True(t, ValidFullName(GitLab, "group/sub.group/my_project"))
	assert.False(t, ValidFullName(GitLab, "single"))
	assert.False(t, ValidFullName("bitbucket", "a/b"))

	_, err := Factory{}.Client(GitHub, "", "a/b", "")
	assert.ErrorIs(t, err, ErrInvalidInput, "a token is required")

	for _, c := range []struct{ raw, want string }{
		{"", ""},
		{"\thttps://git.example.com/ ", "https://git.example.com"}, // surrounding whitespace is trimmed
		{"https://git.example.com/gitlab/", "https://git.example.com/gitlab"},
	} {
		got, err := NormalizeBaseURL(c.raw)
		require.NoError(t, err, c.raw)
		assert.Equal(t, c.want, got)
	}
	for _, bad := range []string{"http://git.example.com", "https://user:pw@git.example.com", "https://git.example.com?x=1", "https://git.example.com#f", "ftp://x", "https://"} {
		_, err := NormalizeBaseURL(bad)
		assert.True(t, errors.Is(err, ErrInvalidInput), bad)
	}
}

func TestWebhookVerification(t *testing.T) {
	secret, body := []byte("s3cret"), []byte(`{"ref":"refs/heads/main"}`)
	sig := SignGitHub(secret, body)
	assert.True(t, VerifyGitHubSignature(secret, body, sig))
	assert.False(t, VerifyGitHubSignature([]byte("wrong"), body, sig))
	assert.False(t, VerifyGitHubSignature(secret, []byte(`{"ref":"refs/heads/evil"}`), sig))
	assert.False(t, VerifyGitHubSignature(secret, body, ""))
	assert.False(t, VerifyGitHubSignature(secret, body, "sha1=abc"))
	assert.False(t, VerifyGitHubSignature(secret, body, "sha256=zz"))

	assert.True(t, VerifyGitLabToken(secret, "s3cret"))
	assert.False(t, VerifyGitLabToken(secret, "s3cre"))
	assert.False(t, VerifyGitLabToken(secret, ""))
}

func TestParseEvents(t *testing.T) {
	h := http.Header{}
	h.Set("X-GitHub-Delivery", "d-1")
	h.Set("X-GitHub-Event", "push")
	ev, err := ParseGitHub(h, []byte(`{"ref":"refs/heads/main","after":"abc123"}`))
	require.NoError(t, err)
	assert.Equal(t, Event{DeliveryID: "d-1", Kind: "push", Ref: "refs/heads/main", CommitSHA: "abc123"}, ev)

	ev, _ = ParseGitHub(h, []byte(`{"ref":"refs/tags/v1.0","after":"def"}`))
	assert.Equal(t, "tag_push", ev.Kind)

	h.Set("X-GitHub-Event", "pull_request")
	ev, _ = ParseGitHub(h, []byte(`{"pull_request":{"head":{"ref":"feature/x","sha":"f00"}}}`))
	assert.Equal(t, Event{DeliveryID: "d-1", Kind: "pull_request", Ref: "refs/heads/feature/x", CommitSHA: "f00"}, ev)

	h.Set("X-GitHub-Event", "ping")
	ev, _ = ParseGitHub(h, []byte(`{"zen":"Keep it simple."}`))
	assert.Equal(t, "ping", ev.Kind)

	_, err = ParseGitHub(h, []byte(`not json`))
	assert.Error(t, err)

	g := http.Header{}
	g.Set("X-Gitlab-Event", "Push Hook")
	g.Set("X-Gitlab-Event-UUID", "u-1")
	ev, _ = ParseGitLab(g, []byte(`{"ref":"refs/heads/main","checkout_sha":"aaa","after":"bbb"}`))
	assert.Equal(t, Event{DeliveryID: "u-1", Kind: "push", Ref: "refs/heads/main", CommitSHA: "aaa"}, ev)

	g.Set("X-Gitlab-Event", "Merge Request Hook")
	ev, _ = ParseGitLab(g, []byte(`{"object_attributes":{"source_branch":"fix","last_commit":{"id":"c0ffee"}}}`))
	assert.Equal(t, Event{DeliveryID: "u-1", Kind: "pull_request", Ref: "refs/heads/fix", CommitSHA: "c0ffee"}, ev)

	// Without a delivery id, the body hash identifies the delivery.
	g.Del("X-Gitlab-Event-UUID")
	g.Set("X-Gitlab-Event", "Tag Push Hook")
	ev, _ = ParseGitLab(g, []byte(`{"ref":"refs/tags/v2","after":"123"}`))
	assert.Equal(t, "tag_push", ev.Kind)
	assert.Contains(t, ev.DeliveryID, "sha256:")
	again, _ := ParseGitLab(g, []byte(`{"ref":"refs/tags/v2","after":"123"}`))
	assert.Equal(t, ev.DeliveryID, again.DeliveryID)
}
