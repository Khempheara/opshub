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
}

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
	Ref        string
	CommitSHA  string
}

// ParseGitHub reads a GitHub webhook's headers and (already verified) JSON body.
func ParseGitHub(h http.Header, body []byte) (Event, error) {
	ev := Event{DeliveryID: h.Get("X-GitHub-Delivery"), Kind: h.Get("X-GitHub-Event")}
	var p struct {
		Ref         string `json:"ref"`
		After       string `json:"after"`
		PullRequest *struct {
			Head struct {
				Ref string `json:"ref"`
				SHA string `json:"sha"`
			} `json:"head"`
		} `json:"pull_request"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return ev, err
	}
	switch ev.Kind {
	case "push":
		ev.Ref, ev.CommitSHA = p.Ref, p.After
		if strings.HasPrefix(p.Ref, "refs/tags/") {
			ev.Kind = "tag_push"
		}
	case "pull_request":
		if p.PullRequest != nil {
			ev.Ref, ev.CommitSHA = "refs/heads/"+p.PullRequest.Head.Ref, p.PullRequest.Head.SHA
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
		Ref         string `json:"ref"`
		CheckoutSHA string `json:"checkout_sha"`
		After       string `json:"after"`
		Attrs       *struct {
			SourceBranch string `json:"source_branch"`
			LastCommit   struct {
				ID string `json:"id"`
			} `json:"last_commit"`
		} `json:"object_attributes"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return ev, err
	}
	switch event := h.Get("X-Gitlab-Event"); event {
	case "Push Hook":
		ev.Kind, ev.Ref, ev.CommitSHA = "push", p.Ref, firstNonEmpty(p.CheckoutSHA, p.After)
	case "Tag Push Hook":
		ev.Kind, ev.Ref, ev.CommitSHA = "tag_push", p.Ref, firstNonEmpty(p.CheckoutSHA, p.After)
	case "Merge Request Hook":
		ev.Kind = "pull_request"
		if p.Attrs != nil {
			ev.Ref, ev.CommitSHA = "refs/heads/"+p.Attrs.SourceBranch, p.Attrs.LastCommit.ID
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
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
