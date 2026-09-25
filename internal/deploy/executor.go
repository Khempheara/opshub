package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Release is one deployment as the executors see it.
type Release struct {
	DeploymentID uuid.UUID
	Version      string // the image to deploy
	Previous     string // the image running before ("" when unknown or first deploy)
	Strategy     string // rolling | blue_green
	Environment  string
	Project      string
}

// HealthResult is one health check, stored with the deployment.
type HealthResult struct {
	Target   string    `json:"target"` // what was checked: a URL, container or rollout
	OK       bool      `json:"ok"`
	Status   int       `json:"status,omitempty"`
	Attempts int       `json:"attempts"`
	Detail   string    `json:"detail,omitempty"`
	At       time.Time `json:"at"`
}

// Outcome of a deploy attempt.
type Outcome struct {
	Health   []HealthResult
	Reverted bool   // a failure put the previous release back
	Previous string // the image that was running (when the executor learned it)
}

// Failure reasons stored on failed deployments.
const (
	ReasonDeployFailed      = "deploy_failed"
	ReasonUnhealthy         = "health_check_failed"
	ReasonUnreachable       = "target_unreachable"
	ReasonHostKeyUntrusted  = "host_key_untrusted"
	ReasonTargetMissing     = "target_missing"
	ReasonInterrupted       = "interrupted"
	ReasonStrategyUnsupport = "strategy_not_supported"
)

// deployError carries a failure reason.
type deployError struct {
	reason string
	err    error
}

func (e *deployError) Error() string { return e.err.Error() }
func (e *deployError) Unwrap() error { return e.err }

func fail(reason string, format string, args ...any) error {
	return &deployError{reason: reason, err: fmt.Errorf(format, args...)}
}

// reasonOf maps an executor error to a stored failure reason.
func reasonOf(err error) string {
	var de *deployError
	if errors.As(err, &de) {
		return de.reason
	}
	return ReasonDeployFailed
}

// Check is one line of a connection test.
type Check struct {
	Name        string `json:"name"`
	OK          bool   `json:"ok"`
	Detail      string `json:"detail,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"` // SSH host key, for pinning
	Pinned      bool   `json:"pinned,omitempty"`      // the fingerprint is already trusted
}

// TestResult is the answer to "Test connection".
type TestResult struct {
	OK     bool    `json:"ok"`
	Checks []Check `json:"checks"`
}

// executor applies releases to one target.
type executor interface {
	Deploy(ctx context.Context, r Release, log io.Writer) (Outcome, error)
	Test(ctx context.Context) TestResult
	Close() error
}

// healthChecker polls a URL until it answers as expected.
type healthChecker struct {
	client   *http.Client
	interval time.Duration
}

// check polls until the expected status or the check's timeout.
func (h healthChecker) check(ctx context.Context, hc HealthCheck, host string, log io.Writer) HealthResult {
	u := strings.ReplaceAll(hc.URL, "{host}", host)
	timeout := time.Duration(hc.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	res := HealthResult{Target: u}
	_, _ = fmt.Fprintf(log, "Health check %s (up to %s)\n", u, timeout)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		res.Attempts++
		status, err := h.get(ctx, u)
		res.Status = status
		switch {
		case err != nil:
			res.Detail = err.Error()
		case (hc.ExpectedStatus == 0 && status >= 200 && status < 300) || status == hc.ExpectedStatus:
			res.OK, res.Detail, res.At = true, "", time.Now()
			_, _ = fmt.Fprintf(log, "Healthy: %s answered %d\n", u, status)
			return res
		default:
			res.Detail = fmt.Sprintf("status %d", status)
		}
		select {
		case <-ctx.Done():
			res.At = time.Now()
			_, _ = fmt.Fprintf(log, "Unhealthy: %s (%s) after %d attempts\n", u, res.Detail, res.Attempts)
			return res
		case <-time.After(h.interval):
		}
	}
}

func (h healthChecker) get(ctx context.Context, u string) (int, error) {
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "OpsHub-HealthCheck")
	resp, err := h.client.Do(req)
	if err != nil {
		return 0, err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}

// shellQuote quotes s for a POSIX shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
