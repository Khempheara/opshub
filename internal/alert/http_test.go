package alert

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/opshub/opshub/internal/authn"
	"github.com/opshub/opshub/internal/config"
	"github.com/opshub/opshub/internal/server"
	"github.com/opshub/opshub/internal/store"
	"github.com/opshub/opshub/internal/telemetry"
	"github.com/opshub/opshub/internal/testutil/pgtest"
)

type noTokens struct{}

func (noTokens) LookupAPIToken(context.Context, []byte) (uuid.UUID, uuid.UUID, []string, error) {
	return uuid.Nil, uuid.Nil, nil, io.EOF
}

type api struct {
	srv    *httptest.Server
	signer *authn.JWTSigner
}

func newAPI(t *testing.T, h server.Module) *api {
	t.Helper()
	signer, err := authn.NewJWTSigner([]config.NamedKey{{ID: "k1", Key: bytes.Repeat([]byte{5}, 32)}}, nil)
	require.NoError(t, err)
	handler := server.New(server.Deps{
		Config:        config.Config{DefaultLocale: "en", DefaultTimezone: "UTC", RateLimitRPS: 1000, RateLimitBurst: 1000},
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:            pgtest.Pool(t),
		Metrics:       telemetry.NewMetrics(),
		Version:       "test",
		Authenticator: &authn.Authenticator{JWT: signer, Tokens: noTokens{}},
		Modules:       []server.Module{h},
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &api{srv: srv, signer: signer}
}

// call sends a request as a user and returns the status and decoded body.
func (a *api) call(t *testing.T, u *user, method, path string, body any, headers map[string]string) (int, map[string]any) {
	t.Helper()
	var rdr io.Reader = http.NoBody
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, a.srv.URL+path, rdr)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	tok, _, err := a.signer.Issue(u.id, uuid.New())
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func errCode(body map[string]any) string {
	e, _ := body["error"].(map[string]any)
	s, _ := e["code"].(string)
	return s
}

// uncovered lists mounted routes missing from covered.
func uncovered(t *testing.T, mount func(chi.Router), covered []string) []string {
	t.Helper()
	r := chi.NewRouter()
	mount(r)
	var missing []string
	require.NoError(t, chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if !slices.Contains(covered, method+" "+route) {
			missing = append(missing, method+" "+route)
		}
		return nil
	}))
	return missing
}

var tenantRoutes = []string{
	"GET /orgs/{orgId}/alert-rules", "POST /orgs/{orgId}/alert-rules", "GET /alert-rules/{ruleId}",
	"PATCH /alert-rules/{ruleId}", "DELETE /alert-rules/{ruleId}", "GET /orgs/{orgId}/alerts", "GET /alerts/{alertId}",
	"POST /alerts/{alertId}/acknowledge", "GET /orgs/{orgId}/silences", "POST /orgs/{orgId}/silences", "DELETE /silences/{silenceId}",
}

func TestTenantIsolation(t *testing.T) {
	e := newEnv(t)
	a := newAPI(t, NewHandler(e.svc))
	e.monitor(t, "web", ptr(false), 0)
	r := e.rule(t, RuleInput{Name: "Down", Kind: store.AlertRuleKindMonitorDown})
	e.tick(t)
	var alertID uuid.UUID
	for _, al := range e.open(t, r.ID) {
		alertID = al.ID
	}
	sl, err := e.svc.CreateSilence(e.dev.ctx, e.orgID, SilenceInput{RuleID: &r.ID, EndsAt: time.Now().Add(time.Hour)})
	require.NoError(t, err)
	o, rid, aid, sid := e.orgID.String(), r.ID.String(), alertID.String(), sl.ID.String()
	cases := []struct {
		method, path string
		body         any
		code         string
	}{
		{"GET", "/api/v1/orgs/" + o + "/alert-rules", nil, "ORG_NOT_FOUND"},
		{"POST", "/api/v1/orgs/" + o + "/alert-rules", map[string]any{"name": "x", "kind": "monitor_down"}, "ORG_NOT_FOUND"},
		{"GET", "/api/v1/alert-rules/" + rid, nil, "ALERT_RULE_NOT_FOUND"},
		{"PATCH", "/api/v1/alert-rules/" + rid, map[string]any{"name": "x"}, "ALERT_RULE_NOT_FOUND"},
		{"DELETE", "/api/v1/alert-rules/" + rid, nil, "ALERT_RULE_NOT_FOUND"},
		{"GET", "/api/v1/orgs/" + o + "/alerts", nil, "ORG_NOT_FOUND"},
		{"GET", "/api/v1/alerts/" + aid, nil, "ALERT_NOT_FOUND"},
		{"POST", "/api/v1/alerts/" + aid + "/acknowledge", nil, "ALERT_NOT_FOUND"},
		{"GET", "/api/v1/orgs/" + o + "/silences", nil, "ORG_NOT_FOUND"},
		{"POST", "/api/v1/orgs/" + o + "/silences", map[string]any{"label": "x", "ends_at": time.Now().Add(time.Hour)}, "ORG_NOT_FOUND"},
		{"DELETE", "/api/v1/silences/" + sid, nil, "SILENCE_NOT_FOUND"},
	}
	require.Len(t, cases, len(tenantRoutes))
	for _, c := range cases {
		status, body := a.call(t, &e.outsider, c.method, c.path, c.body, map[string]string{"If-Match": `"1"`})
		assert.Equal(t, http.StatusNotFound, status, c.path)
		assert.Equal(t, c.code, errCode(body), c.path)
	}
}

func TestRouteCoverage(t *testing.T) {
	assert.Empty(t, uncovered(t, NewHandler(nil).Mount, tenantRoutes))
}

func TestHTTPFlow(t *testing.T) {
	e := newEnv(t)
	a := newAPI(t, NewHandler(e.svc))
	e.monitor(t, "web", ptr(false), 0)
	base := "/api/v1/orgs/" + e.orgID.String()
	status, r := a.call(t, &e.dev, "POST", base+"/alert-rules", map[string]any{"name": "Down", "kind": "monitor_down", "severity": "critical"}, nil)
	require.Equal(t, http.StatusCreated, status, r)
	e.tick(t)
	status, list := a.call(t, &e.viewer, "GET", base+"/alerts?status=firing&severity=critical", nil, nil)
	require.Equal(t, http.StatusOK, status, list)
	items := list["items"].([]any)
	require.Len(t, items, 1)
	id := items[0].(map[string]any)["id"].(string)
	status, body := a.call(t, &e.viewer, "GET", base+"/alerts?status=pending", nil, nil)
	assert.Equal(t, http.StatusUnprocessableEntity, status, body)
	status, body = a.call(t, &e.dev, "POST", "/api/v1/alerts/"+id+"/acknowledge", nil, nil)
	require.Equal(t, http.StatusOK, status, body)
	assert.NotNil(t, body["acknowledged_at"])
	events := body["events"].([]any)
	assert.Equal(t, "acknowledged", events[len(events)-1].(map[string]any)["kind"])
	status, body = a.call(t, &e.dev, "POST", base+"/silences", map[string]any{"severity": "critical", "ends_at": time.Now().Add(time.Hour)}, nil)
	require.Equal(t, http.StatusCreated, status, body)
	status, _ = a.call(t, &e.dev, "DELETE", "/api/v1/silences/"+body["id"].(string), nil, nil)
	assert.Equal(t, http.StatusNoContent, status)
	status, body = a.call(t, &e.viewer, "GET", base+"/silences?include_expired=true", nil, nil)
	require.Equal(t, http.StatusOK, status)
	assert.Len(t, body["items"], 1)
}
