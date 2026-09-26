package gitprovider

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type gitlab struct {
	api      apiClient
	fullName string
}

func gitlabAuth(h http.Header, token string) { h.Set("PRIVATE-TOKEN", token) }

// maintainerAccess is GitLab's access level needed to manage project webhooks.
const maintainerAccess = 40

func (g *gitlab) client() apiClient {
	c := g.api
	c.auth = gitlabAuth
	return c
}

func (g *gitlab) project() string { return "/projects/" + url.PathEscape(g.fullName) }

func (g *gitlab) Repo(ctx context.Context) (Repo, error) {
	type access struct {
		AccessLevel int `json:"access_level"`
	}
	var r struct {
		ID                int64  `json:"id"`
		PathWithNamespace string `json:"path_with_namespace"`
		WebURL            string `json:"web_url"`
		HTTPURLToRepo     string `json:"http_url_to_repo"`
		DefaultBranch     string `json:"default_branch"`
		Permissions       struct {
			Project *access `json:"project_access"`
			Group   *access `json:"group_access"`
		} `json:"permissions"`
	}
	if err := g.client().do(ctx, http.MethodGet, g.project(), nil, &r); err != nil {
		return Repo{}, err
	}
	level := 0
	for _, a := range []*access{r.Permissions.Project, r.Permissions.Group} {
		if a != nil && a.AccessLevel > level {
			level = a.AccessLevel
		}
	}
	return Repo{
		ExternalID: strconv.FormatInt(r.ID, 10), FullName: r.PathWithNamespace, WebURL: r.WebURL,
		CloneURL: r.HTTPURLToRepo, DefaultBranch: r.DefaultBranch, CanManageHooks: level >= maintainerAccess,
	}, nil
}

func (g *gitlab) CreateHook(ctx context.Context, hookURL, secret string) (string, error) {
	in := map[string]any{
		"url": hookURL, "token": secret, "push_events": true, "tag_push_events": true,
		"merge_requests_events": true, "enable_ssl_verification": true,
	}
	var out struct {
		ID int64 `json:"id"`
	}
	if err := g.client().do(ctx, http.MethodPost, g.project()+"/hooks", in, &out); err != nil {
		return "", err
	}
	return strconv.FormatInt(out.ID, 10), nil
}

func (g *gitlab) DeleteHook(ctx context.Context, id string) error {
	err := g.client().do(ctx, http.MethodDelete, g.project()+"/hooks/"+url.PathEscape(id), nil, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

func (g *gitlab) File(ctx context.Context, path, sha string) ([]byte, error) {
	if !validRef(sha) || !validRef(path) {
		return nil, ErrInvalidInput
	}
	return g.client().raw(ctx, g.project()+"/repository/files/"+url.PathEscape(path)+"/raw?ref="+url.QueryEscape(sha), "*/*")
}

func (g *gitlab) Commit(ctx context.Context, ref string) (Commit, error) {
	if !validRef(ref) {
		return Commit{}, ErrInvalidInput
	}
	var out struct {
		ID            string    `json:"id"`
		Message       string    `json:"message"`
		CommittedDate time.Time `json:"committed_date"`
	}
	if err := g.client().do(ctx, http.MethodGet, g.project()+"/repository/commits/"+url.PathEscape(ref), nil, &out); err != nil {
		return Commit{}, err
	}
	return Commit{SHA: out.ID, Message: out.Message, Time: out.CommittedDate}, nil
}

func (g *gitlab) Archive(ctx context.Context, sha string) (io.ReadCloser, error) {
	if !validRef(sha) {
		return nil, ErrInvalidInput
	}
	return g.client().stream(ctx, g.project()+"/repository/archive.tar.gz?sha="+url.QueryEscape(sha), "*/*")
}
