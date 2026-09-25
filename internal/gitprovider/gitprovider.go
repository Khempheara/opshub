// Package gitprovider talks to GitHub and GitLab (cloud or self-hosted) with a personal or
// project access token: it reads repository metadata, creates and deletes webhooks, and
// verifies and parses incoming webhook requests. It uses only net/http; outbound requests go
// through the SSRF-safe client from internal/safehttp.
package gitprovider

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/opshub/opshub/internal/safehttp"
)

// Provider identifies a Git host type.
type Provider string

const (
	GitHub Provider = "github"
	GitLab Provider = "gitlab"
)

// Errors returned by clients. Callers map them to API error codes.
var (
	ErrNotFound     = errors.New("repository not found or not visible to the token")
	ErrUnauthorized = errors.New("access token rejected")
	ErrForbidden    = errors.New("access token lacks the required permission")
	ErrUnavailable  = errors.New("git provider unavailable")
	ErrInvalidInput = errors.New("invalid repository reference")
)

// Repo is the repository metadata OpsHub stores.
type Repo struct {
	ExternalID    string
	FullName      string
	WebURL        string
	CloneURL      string
	DefaultBranch string
	// CanManageHooks: the token may create webhooks (GitHub admin, GitLab Maintainer+).
	CanManageHooks bool
}

// Client is a repository-scoped API client.
type Client interface {
	Repo(ctx context.Context) (Repo, error)
	CreateHook(ctx context.Context, url, secret string) (id string, err error)
	DeleteHook(ctx context.Context, id string) error
	// File returns a file's content at a commit (ErrNotFound when it doesn't exist).
	File(ctx context.Context, path, sha string) ([]byte, error)
	// Commit resolves a branch, tag or SHA to a commit (ErrNotFound when unknown).
	Commit(ctx context.Context, ref string) (Commit, error)
	// Archive streams a gzip-compressed tarball of the repository at a commit. Entries sit
	// under one top-level directory (as GitHub and GitLab produce them).
	Archive(ctx context.Context, sha string) (io.ReadCloser, error)
}

// Commit is a resolved ref.
type Commit struct {
	SHA     string
	Message string
}

// MaxFileSize bounds files read from repositories (pipeline definitions are small).
const MaxFileSize = 1 << 20

// Factory builds clients. The cloud API URLs are overridable for tests.
type Factory struct {
	HTTP         *http.Client
	GitHubAPIURL string // default https://api.github.com
	GitLabURL    string // default https://gitlab.com
}

var (
	githubName = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}/[A-Za-z0-9_.-]{1,100}$`)
	gitlabPath = regexp.MustCompile(`^[A-Za-z0-9_.-]+(/[A-Za-z0-9_.-]+)+$`)
)

// NormalizeBaseURL validates a self-hosted instance URL: https, no credentials, query or
// fragment. It returns the URL without a trailing slash; "" stays "" (the cloud service).
func NormalizeBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("%w: base URL must be an https URL without credentials, query or fragment", ErrInvalidInput)
	}
	return strings.TrimRight(u.Scheme+"://"+u.Host+u.EscapedPath(), "/"), nil
}

// ValidFullName reports whether name is a valid repository reference for the provider
// ("owner/repo" on GitHub, "group/subgroup/project" on GitLab).
func ValidFullName(p Provider, name string) bool {
	if len(name) > 255 {
		return false
	}
	switch p {
	case GitHub:
		return githubName.MatchString(name) && !strings.Contains(name, "..")
	case GitLab:
		return gitlabPath.MatchString(name) && !strings.Contains(name, "..")
	}
	return false
}

// Client returns a client for one repository. baseURL is a self-hosted instance ("" for
// github.com / gitlab.com) and must already be normalized.
func (f Factory) Client(p Provider, baseURL, fullName, token string) (Client, error) {
	if !ValidFullName(p, fullName) || token == "" {
		return nil, ErrInvalidInput
	}
	api := apiClient{http: f.HTTP, token: token}
	switch p {
	case GitHub:
		api.base = f.GitHubAPIURL
		if api.base == "" {
			api.base = "https://api.github.com"
		}
		if baseURL != "" {
			api.base = baseURL + "/api/v3" // GitHub Enterprise Server
		}
		return &github{api: api, fullName: fullName}, nil
	case GitLab:
		root := f.GitLabURL
		if root == "" {
			root = "https://gitlab.com"
		}
		if baseURL != "" {
			root = baseURL
		}
		api.base = root + "/api/v4"
		return &gitlab{api: api, fullName: fullName}, nil
	}
	return nil, ErrInvalidInput
}

// apiClient sends authenticated JSON requests and maps HTTP failures to the errors above.
type apiClient struct {
	http  *http.Client
	base  string
	token string
	auth  func(h http.Header, token string)
}

const maxResponse = 1 << 20

func (c apiClient) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidInput, err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "OpsHub")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c.auth(req.Header, c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		if safehttp.IsBlocked(err) {
			return err
		}
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _, err := safehttp.ReadLimited(resp.Body, maxResponse)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return ErrUnauthorized
	case resp.StatusCode == http.StatusForbidden:
		return ErrForbidden
	case resp.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case resp.StatusCode == http.StatusUnprocessableEntity || resp.StatusCode == http.StatusBadRequest:
		// e.g. GitLab rejects webhook URLs that point at local addresses.
		return fmt.Errorf("%w: provider rejected the request (%d)", ErrForbidden, resp.StatusCode)
	case resp.StatusCode >= 300:
		return fmt.Errorf("%w: status %d", ErrUnavailable, resp.StatusCode)
	}
	if out != nil && len(b) > 0 {
		if err := json.Unmarshal(b, out); err != nil {
			return fmt.Errorf("%w: unexpected response: %w", ErrUnavailable, err)
		}
	}
	return nil
}

// raw fetches a non-JSON body (file contents) with the given Accept header.
func (c apiClient) raw(ctx context.Context, path, accept string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidInput, err)
	}
	req.Header.Set("User-Agent", "OpsHub")
	c.auth(req.Header, c.token)
	req.Header.Set("Accept", accept)
	resp, err := c.http.Do(req)
	if err != nil {
		if safehttp.IsBlocked(err) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, ErrUnauthorized
	case resp.StatusCode == http.StatusForbidden:
		return nil, ErrForbidden
	case resp.StatusCode == http.StatusNotFound:
		return nil, ErrNotFound
	case resp.StatusCode >= 300:
		return nil, fmt.Errorf("%w: status %d", ErrUnavailable, resp.StatusCode)
	}
	b, cut, err := safehttp.ReadLimited(resp.Body, MaxFileSize)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if cut {
		return nil, ErrFileTooLarge
	}
	return b, nil
}

// stream opens a GET that may redirect (archives redirect to a signed download URL). It
// follows up to 3 redirects itself: the SSRF-safe client refuses to, the dial-time address
// check still applies to every hop, and credentials are only sent to the API host.
func (c apiClient) stream(ctx context.Context, path, accept string) (io.ReadCloser, error) {
	target := c.base + path
	// Never let the client follow redirects on its own: Go would forward credentials to the
	// same hostname on another port.
	hc := *c.http
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	apiHost := ""
	if u, err := url.Parse(c.base); err == nil {
		apiHost = u.Host
	}
	for hop := 0; hop <= 3; hop++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidInput, err)
		}
		req.Header.Set("User-Agent", "OpsHub")
		req.Header.Set("Accept", accept)
		if req.URL.Host == apiHost {
			c.auth(req.Header, c.token)
			req.Header.Set("Accept", accept)
		}
		resp, err := hc.Do(req)
		if err != nil {
			if safehttp.IsBlocked(err) {
				return nil, err
			}
			return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		switch {
		case resp.StatusCode >= 300 && resp.StatusCode < 400:
			loc, lerr := resp.Location()
			_ = resp.Body.Close()
			if lerr != nil || (loc.Scheme != "https" && loc.Scheme != req.URL.Scheme) {
				return nil, fmt.Errorf("%w: bad redirect", ErrUnavailable)
			}
			target = loc.String()
			continue
		case resp.StatusCode == http.StatusUnauthorized:
			_ = resp.Body.Close()
			return nil, ErrUnauthorized
		case resp.StatusCode == http.StatusForbidden:
			_ = resp.Body.Close()
			return nil, ErrForbidden
		case resp.StatusCode == http.StatusNotFound:
			_ = resp.Body.Close()
			return nil, ErrNotFound
		case resp.StatusCode >= 300:
			_ = resp.Body.Close()
			return nil, fmt.Errorf("%w: status %d", ErrUnavailable, resp.StatusCode)
		}
		return resp.Body, nil
	}
	return nil, fmt.Errorf("%w: too many redirects", ErrUnavailable)
}

// ErrFileTooLarge is returned by File for files over MaxFileSize.
var ErrFileTooLarge = errors.New("file too large")

// validRef rejects refs that could change the API path.
func validRef(ref string) bool {
	return ref != "" && len(ref) <= 255 && !strings.Contains(ref, "..") && !strings.ContainsAny(ref, " \t\n?#%\\")
}

// ---- webhook verification and parsing (no network) ----

// VerifyGitHubSignature checks X-Hub-Signature-256 ("sha256=<hex HMAC of the body>") in
// constant time.
func VerifyGitHubSignature(secret, body []byte, header string) bool {
	sig, ok := strings.CutPrefix(header, "sha256=")
	if !ok {
		return false
	}
	got, err := hex.DecodeString(sig)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}

// SignGitHub computes the X-Hub-Signature-256 value for a body (used by tests and tooling).
func SignGitHub(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// VerifyGitLabToken checks X-Gitlab-Token in constant time (hashing first so the comparison
// doesn't leak the secret's length).
func VerifyGitLabToken(secret []byte, header string) bool {
	a := sha256.Sum256(secret)
	b := sha256.Sum256([]byte(header))
	return header != "" && subtle.ConstantTimeCompare(a[:], b[:]) == 1
}

// Event is what OpsHub records about an incoming webhook.
type Event struct {
	DeliveryID string
	Kind       string // push, tag_push, pull_request, ping, or the provider's event name
	Ref        string // refs/heads/… or refs/tags/…; for pull requests the source branch
	CommitSHA  string
	// Pull requests: normalized action (opened, synchronize, reopened, closed, edited) and
	// the target branch.
	Action  string
	BaseRef string
	Deleted bool   // a branch or tag was deleted (no commit to build)
	Title   string // head commit message (first line) or pull request title
	Actor   string // Git host username
}

// Buildable reports whether the event can start a pipeline run.
func (e Event) Buildable() bool {
	switch e.Kind {
	case "push", "tag_push":
		return !e.Deleted && e.CommitSHA != ""
	case "pull_request":
		return e.CommitSHA != "" && (e.Action == "opened" || e.Action == "synchronize" || e.Action == "reopened")
	}
	return false
}

const zeroSHA = "0000000000000000000000000000000000000000"

// ParseGitHub reads a GitHub webhook's headers and (already verified) JSON body.
func ParseGitHub(h http.Header, body []byte) (Event, error) {
	ev := Event{DeliveryID: h.Get("X-GitHub-Delivery"), Kind: h.Get("X-GitHub-Event")}
	var p struct {
		Ref        string `json:"ref"`
		After      string `json:"after"`
		Deleted    bool   `json:"deleted"`
		Action     string `json:"action"`
		HeadCommit *struct {
			Message string `json:"message"`
		} `json:"head_commit"`
		Sender struct {
			Login string `json:"login"`
		} `json:"sender"`
		PullRequest *struct {
			Title string `json:"title"`
			Head  struct {
				Ref string `json:"ref"`
				SHA string `json:"sha"`
			} `json:"head"`
			Base struct {
				Ref string `json:"ref"`
			} `json:"base"`
		} `json:"pull_request"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return ev, err
	}
	ev.Actor = p.Sender.Login
	switch ev.Kind {
	case "push":
		ev.Ref, ev.CommitSHA, ev.Deleted = p.Ref, p.After, p.Deleted || p.After == zeroSHA
		if strings.HasPrefix(p.Ref, "refs/tags/") {
			ev.Kind = "tag_push"
		}
		if p.HeadCommit != nil {
			ev.Title = p.HeadCommit.Message
		}
	case "pull_request":
		ev.Action = p.Action
		if p.PullRequest != nil {
			ev.Ref, ev.CommitSHA = "refs/heads/"+p.PullRequest.Head.Ref, p.PullRequest.Head.SHA
			ev.BaseRef, ev.Title = p.PullRequest.Base.Ref, p.PullRequest.Title
		}
	}
	finish(&ev, body)
	return ev, nil
}

// ParseGitLab reads a GitLab webhook's headers and (already verified) JSON body.
func ParseGitLab(h http.Header, body []byte) (Event, error) {
	ev := Event{DeliveryID: h.Get("X-Gitlab-Event-UUID")}
	if ev.DeliveryID == "" {
		ev.DeliveryID = h.Get("Idempotency-Key")
	}
	var p struct {
		Ref          string `json:"ref"`
		CheckoutSHA  string `json:"checkout_sha"`
		After        string `json:"after"`
		UserUsername string `json:"user_username"`
		User         struct {
			Username string `json:"username"`
		} `json:"user"`
		Commits []struct {
			ID      string `json:"id"`
			Message string `json:"message"`
		} `json:"commits"`
		Attrs *struct {
			SourceBranch string `json:"source_branch"`
			TargetBranch string `json:"target_branch"`
			Title        string `json:"title"`
			Action       string `json:"action"`
			OldRev       string `json:"oldrev"`
			LastCommit   struct {
				ID string `json:"id"`
			} `json:"last_commit"`
		} `json:"object_attributes"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return ev, err
	}
	pushTitle := func(sha string) string {
		for _, c := range p.Commits {
			if c.ID == sha {
				return c.Message
			}
		}
		if n := len(p.Commits); n > 0 {
			return p.Commits[n-1].Message
		}
		return ""
	}
	switch event := h.Get("X-Gitlab-Event"); event {
	case "Push Hook", "Tag Push Hook":
		ev.Kind = "push"
		if event == "Tag Push Hook" {
			ev.Kind = "tag_push"
		}
		ev.Ref, ev.CommitSHA = p.Ref, firstNonEmpty(p.CheckoutSHA, p.After)
		ev.Deleted = p.After == zeroSHA || ev.CommitSHA == ""
		ev.Title, ev.Actor = pushTitle(ev.CommitSHA), p.UserUsername
	case "Merge Request Hook":
		ev.Kind, ev.Actor = "pull_request", p.User.Username
		if a := p.Attrs; a != nil {
			ev.Ref, ev.CommitSHA = "refs/heads/"+a.SourceBranch, a.LastCommit.ID
			ev.BaseRef, ev.Title = a.TargetBranch, a.Title
			switch a.Action {
			case "open":
				ev.Action = "opened"
			case "reopen":
				ev.Action = "reopened"
			case "update":
				ev.Action = "edited" // title or description only
				if a.OldRev != "" {
					ev.Action = "synchronize" // new commits
				}
			default:
				ev.Action = a.Action
			}
		}
	default:
		ev.Kind = event
	}
	finish(&ev, body)
	return ev, nil
}

// finish fills a missing delivery id from the body hash (so exact redeliveries still dedupe)
// and bounds field lengths to the database limits.
func finish(ev *Event, body []byte) {
	if ev.DeliveryID == "" {
		sum := sha256.Sum256(body)
		ev.DeliveryID = "sha256:" + hex.EncodeToString(sum[:])
	}
	if ev.Kind == "" {
		ev.Kind = "unknown"
	}
	ev.DeliveryID = truncate(ev.DeliveryID, 200)
	ev.Kind = truncate(ev.Kind, 100)
	ev.Ref = truncate(ev.Ref, 255)
	ev.CommitSHA = truncate(ev.CommitSHA, 64)
	ev.BaseRef = truncate(ev.BaseRef, 255)
	ev.Action = truncate(ev.Action, 50)
	ev.Actor = truncate(ev.Actor, 100)
	ev.Title = truncate(firstLineOf(ev.Title), 200)
}

func firstLineOf(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

// truncate cuts s to at most n bytes without splitting a UTF-8 character.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
