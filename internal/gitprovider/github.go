package gitprovider

import (
	"context"
	"errors"
	"net/http"
	"strconv"
)

type github struct {
	api      apiClient
	fullName string
}

func githubAuth(h http.Header, token string) {
	h.Set("Authorization", "Bearer "+token)
	h.Set("Accept", "application/vnd.github+json")
	h.Set("X-GitHub-Api-Version", "2022-11-28")
}

func (g *github) client() apiClient {
	c := g.api
	c.auth = githubAuth
	return c
}

func (g *github) Repo(ctx context.Context) (Repo, error) {
	var r struct {
		ID            int64  `json:"id"`
		FullName      string `json:"full_name"`
		HTMLURL       string `json:"html_url"`
		CloneURL      string `json:"clone_url"`
		DefaultBranch string `json:"default_branch"`
		Permissions   struct {
			Admin bool `json:"admin"`
		} `json:"permissions"`
	}
	if err := g.client().do(ctx, http.MethodGet, "/repos/"+g.fullName, nil, &r); err != nil {
		// GitHub answers 404 (not 403) for private repositories the token can't see.
		return Repo{}, err
	}
	return Repo{
		ExternalID: strconv.FormatInt(r.ID, 10), FullName: r.FullName, WebURL: r.HTMLURL,
		CloneURL: r.CloneURL, DefaultBranch: r.DefaultBranch, CanManageHooks: r.Permissions.Admin,
	}, nil
}

func (g *github) CreateHook(ctx context.Context, url, secret string) (string, error) {
	in := map[string]any{
		"name":   "web",
		"active": true,
		"events": []string{"push", "pull_request"},
		"config": map[string]string{"url": url, "content_type": "json", "secret": secret, "insecure_ssl": "0"},
	}
	var out struct {
		ID int64 `json:"id"`
	}
	if err := g.client().do(ctx, http.MethodPost, "/repos/"+g.fullName+"/hooks", in, &out); err != nil {
		if errors.Is(err, ErrNotFound) {
			return "", ErrForbidden // hooks need admin access; GitHub hides the endpoint otherwise
		}
		return "", err
	}
	return strconv.FormatInt(out.ID, 10), nil
}

func (g *github) DeleteHook(ctx context.Context, id string) error {
	err := g.client().do(ctx, http.MethodDelete, "/repos/"+g.fullName+"/hooks/"+id, nil, nil)
	if errors.Is(err, ErrNotFound) {
		return nil // already gone
	}
	return err
}
