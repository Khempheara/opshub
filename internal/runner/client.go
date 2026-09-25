package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// APIError is an error answer from OpsHub.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string { return fmt.Sprintf("%d %s: %s", e.Status, e.Code, e.Message) }

// IsCode reports whether err is an APIError with code.
func IsCode(err error, code string) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Code == code
}

// Client calls the OpsHub runner API.
type Client struct {
	base      string // https://opshub.example.com/api/v1
	http      *http.Client
	userAgent string
}

// NewClient returns a client for the OpsHub instance at baseURL. The HTTP client has no
// overall timeout (long polls and uploads); every call passes a context.
func NewClient(baseURL, version string) *Client {
	return &Client{
		base: strings.TrimRight(baseURL, "/") + "/api/v1",
		http: &http.Client{Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			ResponseHeaderTimeout: 60 * time.Second,
			IdleConnTimeout:       90 * time.Second,
			MaxIdleConnsPerHost:   16,
		}},
		userAgent: "opshub-runner/" + version,
	}
}

func (c *Client) request(ctx context.Context, method, path, token string, body io.Reader, contentType string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept-Language", "en")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		defer func() { _ = resp.Body.Close() }()
		var env struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&env)
		return nil, &APIError{Status: resp.StatusCode, Code: env.Error.Code, Message: env.Error.Message}
	}
	return resp, nil
}

func (c *Client) jsonCall(ctx context.Context, method, path, token string, in, out any) (int, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return 0, err
		}
		body = bytes.NewReader(b)
	}
	resp, err := c.request(ctx, method, path, token, body, "application/json")
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if out != nil && resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return resp.StatusCode, fmt.Errorf("decode %s: %w", path, err)
		}
	}
	return resp.StatusCode, nil
}

// Registration is the answer to Register.
type Registration struct {
	ID                       string   `json:"id"`
	Token                    string   `json:"token"`
	Name                     string   `json:"name"`
	Labels                   []string `json:"labels"`
	HeartbeatIntervalSeconds int      `json:"heartbeat_interval_seconds"`
}

// RegisterInput is sent by Register.
type RegisterInput struct {
	Token          string   `json:"token"`
	Name           string   `json:"name"`
	Labels         []string `json:"labels"`
	Version        string   `json:"version"`
	OS             string   `json:"os"`
	Arch           string   `json:"arch"`
	MaxConcurrency int      `json:"max_concurrency"`
}

// Register exchanges a registration token for a runner token.
func (c *Client) Register(ctx context.Context, in RegisterInput) (Registration, error) {
	var out Registration
	_, err := c.jsonCall(ctx, http.MethodPost, "/runner/register", "", in, &out)
	return out, err
}

// Heartbeat reports running jobs and returns those to stop.
func (c *Client) Heartbeat(ctx context.Context, token, version, os, arch string, running []string) ([]string, error) {
	var out struct {
		CancelJobIDs []string `json:"cancel_job_ids"`
	}
	if running == nil {
		running = []string{}
	}
	_, err := c.jsonCall(ctx, http.MethodPost, "/runner/heartbeat", token,
		map[string]any{"version": version, "os": os, "arch": arch, "running_job_ids": running}, &out)
	return out.CancelJobIDs, err
}

// InfraHeartbeat sends an infra agent's sample; it returns the interval the server asks for.
func (c *Client) InfraHeartbeat(ctx context.Context, token string, body map[string]any) (int, error) {
	var out struct {
		IntervalSeconds int `json:"interval_seconds"`
	}
	_, err := c.jsonCall(ctx, http.MethodPost, "/agent/heartbeat", token, body, &out)
	return out.IntervalSeconds, err
}

// Job is an assigned job (the fields the executor uses).
type Job struct {
	Job struct {
		ID             string `json:"id"`
		Name           string `json:"name"`
		Attempt        int    `json:"attempt"`
		TimeoutSeconds int    `json:"timeout_seconds"`
	} `json:"job"`
	Spec struct {
		Image     string `json:"image"`
		Steps     []Step `json:"steps"`
		Artifacts *struct {
			Paths []string `json:"paths"`
		} `json:"artifacts"`
		Cache *struct {
			Key   string   `json:"key"`
			Paths []string `json:"paths"`
		} `json:"cache"`
		Needs []string `json:"needs"`
	} `json:"spec"`
	RunID     string            `json:"run_id"`
	RunNumber int               `json:"run_number"`
	ProjectID string            `json:"project_id"`
	CommitSHA string            `json:"commit_sha"`
	Variables map[string]string `json:"variables"`
	Token     string            `json:"token"`
	Secrets   map[string]string `json:"secrets"`
	Masks     []string          `json:"masks"`
}

// Step is one shell command.
type Step struct {
	Name string `json:"name"`
	Run  string `json:"run"`
}

// RequestJob long-polls for a job; nil when none is available.
func (c *Client) RequestJob(ctx context.Context, token string, wait time.Duration) (*Job, error) {
	var out Job
	status, err := c.jsonCall(ctx, http.MethodPost, "/runner/jobs/request", token, map[string]int{"wait_seconds": int(wait / time.Second)}, &out)
	if err != nil || status == http.StatusNoContent {
		return nil, err
	}
	return &out, nil
}

// StepStatus reports a step starting or finishing.
func (c *Client) StepStatus(ctx context.Context, j *Job, index int, status string, exitCode *int) error {
	_, err := c.jsonCall(ctx, http.MethodPatch, "/runner/jobs/"+j.Job.ID, j.Token,
		map[string]any{"step": map[string]any{"index": index, "status": status, "exit_code": exitCode}}, nil)
	return err
}

// Complete reports the job's result. reason is step_failed, timeout or runner_error.
func (c *Client) Complete(ctx context.Context, j *Job, success bool, exitCode *int, reason string) error {
	body := map[string]any{"success": success, "exit_code": exitCode}
	if !success && reason != "" {
		body["reason"] = reason
	}
	_, err := c.jsonCall(ctx, http.MethodPatch, "/runner/jobs/"+j.Job.ID, j.Token, map[string]any{"complete": body}, nil)
	return err
}

// AppendLog sends one log chunk.
func (c *Client) AppendLog(ctx context.Context, j *Job, seq int, content string) error {
	_, err := c.jsonCall(ctx, http.MethodPost, "/runner/jobs/"+j.Job.ID+"/logs", j.Token, map[string]any{"seq": seq, "content": content}, nil)
	return err
}

// Source opens the job's commit tarball; nil (no error) when the project has no repository.
func (c *Client) Source(ctx context.Context, j *Job) (io.ReadCloser, error) {
	resp, err := c.request(ctx, http.MethodGet, "/runner/jobs/"+j.Job.ID+"/source", j.Token, nil, "")
	if IsCode(err, "REPOSITORY_NOT_FOUND") {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// Artifact is an uploaded artifact.
type Artifact struct {
	ID      string `json:"id"`
	JobName string `json:"job_name"`
	Name    string `json:"name"`
}

// UploadArtifacts sends the job's artifact archive (gzip-compressed tar).
func (c *Client) UploadArtifacts(ctx context.Context, j *Job, body io.Reader) error {
	resp, err := c.request(ctx, http.MethodPost, "/runner/jobs/"+j.Job.ID+"/artifacts?name="+url.QueryEscape(j.Job.Name+".tar.gz"),
		j.Token, body, "application/gzip")
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.Body.Close()
}

// Dependencies lists artifacts of the jobs this job needs.
func (c *Client) Dependencies(ctx context.Context, j *Job) ([]Artifact, error) {
	var out struct {
		Items []Artifact `json:"items"`
	}
	_, err := c.jsonCall(ctx, http.MethodGet, "/runner/jobs/"+j.Job.ID+"/dependencies", j.Token, nil, &out)
	return out.Items, err
}

// Dependency opens one dependency artifact.
func (c *Client) Dependency(ctx context.Context, j *Job, artifactID string) (io.ReadCloser, error) {
	resp, err := c.request(ctx, http.MethodGet, "/runner/jobs/"+j.Job.ID+"/dependencies/"+url.PathEscape(artifactID), j.Token, nil, "")
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// Cache opens the project's cache archive for key; nil when there is none.
func (c *Client) Cache(ctx context.Context, j *Job, key string) (io.ReadCloser, error) {
	resp, err := c.request(ctx, http.MethodGet, "/runner/jobs/"+j.Job.ID+"/cache/"+url.PathEscape(key), j.Token, nil, "")
	if IsCode(err, "CACHE_NOT_FOUND") {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// PutCache stores the project's cache archive for key.
func (c *Client) PutCache(ctx context.Context, j *Job, key string, body io.Reader) error {
	resp, err := c.request(ctx, http.MethodPut, "/runner/jobs/"+j.Job.ID+"/cache/"+url.PathEscape(key), j.Token, body, "application/gzip")
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.Body.Close()
}
