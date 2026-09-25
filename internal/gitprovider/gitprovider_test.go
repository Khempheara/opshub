package gitprovider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

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

func TestFileAndCommit(t *testing.T) {
	f := &fakeHost{t: t}
	f.handle = func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.EscapedPath() {
		case "GET /repos/acme/api/contents/.opshub.yml":
			assert.Equal(t, "abc123", r.URL.Query().Get("ref"))
			assert.Equal(t, "application/vnd.github.raw+json", r.Header.Get("Accept"))
			_, _ = w.Write([]byte("version: 1\n"))
		case "GET /repos/acme/api/contents/big.yml":
			_, _ = w.Write(make([]byte, MaxFileSize+1))
		case "GET /repos/acme/api/commits/main":
			writeJSON(w, http.StatusOK, map[string]any{"sha": "abc123", "commit": map[string]string{"message": "Fix it\n\nbody"}})
		case "GET /repos/acme/api/commits/nope":
			w.WriteHeader(http.StatusUnprocessableEntity)
		case "GET /api/v4/projects/grp%2Fapp/repository/files/.opshub.yml/raw":
			assert.Equal(t, "def456", r.URL.Query().Get("ref"))
			_, _ = w.Write([]byte("version: 1\n# gitlab\n"))
		case "GET /api/v4/projects/grp%2Fapp/repository/commits/release%2F1.0":
			writeJSON(w, http.StatusOK, map[string]any{"id": "def456", "message": "Release"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}
	srv := f.server()
	fac := Factory{HTTP: srv.Client(), GitHubAPIURL: srv.URL, GitLabURL: srv.URL}
	ctx := context.Background()

	gh, _ := fac.Client(GitHub, "", "acme/api", "t")
	b, err := gh.File(ctx, ".opshub.yml", "abc123")
	require.NoError(t, err)
	assert.Equal(t, "version: 1\n", string(b))
	_, err = gh.File(ctx, "missing.yml", "abc123")
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = gh.File(ctx, "big.yml", "abc123")
	assert.ErrorIs(t, err, ErrFileTooLarge)
	_, err = gh.File(ctx, ".opshub.yml", "../x")
	assert.ErrorIs(t, err, ErrInvalidInput)
	c, err := gh.Commit(ctx, "main")
	require.NoError(t, err)
	assert.Equal(t, Commit{SHA: "abc123", Message: "Fix it\n\nbody"}, c)
	_, err = gh.Commit(ctx, "nope")
	assert.ErrorIs(t, err, ErrNotFound)

	gl, _ := fac.Client(GitLab, "", "grp/app", "t")
	b, err = gl.File(ctx, ".opshub.yml", "def456")
	require.NoError(t, err)
	assert.Contains(t, string(b), "gitlab")
	c, err = gl.Commit(ctx, "release/1.0")
	require.NoError(t, err)
	assert.Equal(t, "def456", c.SHA)
}

func TestEventDetails(t *testing.T) {
	h := http.Header{}
	h.Set("X-GitHub-Event", "push")
	ev, _ := ParseGitHub(h, []byte(`{"ref":"refs/heads/main","after":"abc","head_commit":{"message":"Add login\n\nlong body"},"sender":{"login":"dara"}}`))
	assert.Equal(t, "Add login", ev.Title)
	assert.Equal(t, "dara", ev.Actor)
	assert.True(t, ev.Buildable())

	ev, _ = ParseGitHub(h, []byte(`{"ref":"refs/heads/old","after":"0000000000000000000000000000000000000000","deleted":true}`))
	assert.True(t, ev.Deleted)
	assert.False(t, ev.Buildable(), "branch deletions don't build")

	h.Set("X-GitHub-Event", "pull_request")
	ev, _ = ParseGitHub(h, []byte(`{"action":"synchronize","pull_request":{"title":"Faster","head":{"ref":"perf","sha":"f00"},"base":{"ref":"main"}}}`))
	assert.Equal(t, Event{DeliveryID: ev.DeliveryID, Kind: "pull_request", Ref: "refs/heads/perf", CommitSHA: "f00", Action: "synchronize", BaseRef: "main", Title: "Faster"}, ev)
	assert.True(t, ev.Buildable())
	ev, _ = ParseGitHub(h, []byte(`{"action":"closed","pull_request":{"head":{"ref":"perf","sha":"f00"},"base":{"ref":"main"}}}`))
	assert.False(t, ev.Buildable())

	g := http.Header{}
	g.Set("X-Gitlab-Event", "Merge Request Hook")
	ev, _ = ParseGitLab(g, []byte(`{"user":{"username":"vicheka"},"object_attributes":{"action":"update","oldrev":"a1","source_branch":"fix","target_branch":"main","title":"Fix","last_commit":{"id":"c0"}}}`))
	assert.Equal(t, "synchronize", ev.Action, "an update with new commits")
	assert.Equal(t, "vicheka", ev.Actor)
	assert.Equal(t, "main", ev.BaseRef)
	ev, _ = ParseGitLab(g, []byte(`{"object_attributes":{"action":"update","source_branch":"fix","target_branch":"main","last_commit":{"id":"c0"}}}`))
	assert.Equal(t, "edited", ev.Action, "a title edit doesn't build")
	assert.False(t, ev.Buildable())

	g.Set("X-Gitlab-Event", "Push Hook")
	ev, _ = ParseGitLab(g, []byte(`{"ref":"refs/heads/main","checkout_sha":"b2","user_username":"sokha","commits":[{"id":"a1","message":"first"},{"id":"b2","message":"second\nmore"}]}`))
	assert.Equal(t, "second", ev.Title)
	assert.Equal(t, "sokha", ev.Actor)
	ev, _ = ParseGitLab(g, []byte(`{"ref":"refs/heads/gone","after":"0000000000000000000000000000000000000000","checkout_sha":null}`))
	assert.False(t, ev.Buildable())

	// Titles are cut without breaking UTF-8 (Khmer is 3 bytes per character).
	long := strings.Repeat("ក", 100)
	h.Set("X-GitHub-Event", "push")
	ev, _ = ParseGitHub(h, []byte(`{"ref":"refs/heads/main","after":"abc","head_commit":{"message":"`+long+`"}}`))
	assert.True(t, utf8.ValidString(ev.Title))
	assert.LessOrEqual(t, len(ev.Title), 200)
}

func TestArchiveFollowsRedirectWithoutLeakingToken(t *testing.T) {
	var downloadAuth string
	download := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downloadAuth = r.Header.Get("Authorization") + r.Header.Get("PRIVATE-TOKEN")
		_, _ = w.Write([]byte("tarball-bytes"))
	}))
	t.Cleanup(download.Close)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/acme/api/tarball/abc123":
			assert.Equal(t, "Bearer secret", r.Header.Get("Authorization"))
			http.Redirect(w, r, download.URL+"/signed?token=xyz", http.StatusFound)
		case "/repos/acme/loop/tarball/abc123":
			http.Redirect(w, r, r.URL.String(), http.StatusFound)
		case "/api/v4/projects/grp%2Fapp/repository/archive.tar.gz", "/api/v4/projects/grp/app/repository/archive.tar.gz":
			assert.Equal(t, "abc123", r.URL.Query().Get("sha"))
			_, _ = w.Write([]byte("gitlab-tarball"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(api.Close)
	fac := Factory{HTTP: api.Client(), GitHubAPIURL: api.URL, GitLabURL: api.URL}
	ctx := context.Background()

	gh, _ := fac.Client(GitHub, "", "acme/api", "secret")
	rc, err := gh.Archive(ctx, "abc123")
	require.NoError(t, err)
	b, _ := io.ReadAll(rc)
	_ = rc.Close()
	assert.Equal(t, "tarball-bytes", string(b))
	assert.Empty(t, downloadAuth, "the Git token isn't sent to the download host")

	loop, _ := fac.Client(GitHub, "", "acme/loop", "secret")
	_, err = loop.Archive(ctx, "abc123")
	assert.ErrorIs(t, err, ErrUnavailable)
	missing, _ := fac.Client(GitHub, "", "acme/missing", "secret")
	_, err = missing.Archive(ctx, "abc123")
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = gh.Archive(ctx, "../x")
	assert.ErrorIs(t, err, ErrInvalidInput)

	gl, _ := fac.Client(GitLab, "", "grp/app", "secret")
	rc, err = gl.Archive(ctx, "abc123")
	require.NoError(t, err)
	b, _ = io.ReadAll(rc)
	_ = rc.Close()
	assert.Equal(t, "gitlab-tarball", string(b))
}
