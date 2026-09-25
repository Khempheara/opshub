package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/opshub/opshub/internal/safehttp"
)

var loopback = safehttp.Options{AllowedCIDRs: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}}

func testHealth() healthChecker {
	return healthChecker{client: safehttp.NewClient(loopback), interval: 50 * time.Millisecond}
}

// versionServer answers 200 while the deployed version (a file written by the deploy
// command) isn't "bad".
func versionServer(t *testing.T, file string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		b, _ := os.ReadFile(file) // #nosec G304 -- test file
		if strings.Contains(string(b), "bad") {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func sshExec(t *testing.T, srv *sshServer, cfg SSHConfig) *sshExecutor {
	t.Helper()
	d, err := newSSHDialer(loopback.Dialer(), "deploy", SSHCredentials{PrivateKey: srv.clientKey})
	require.NoError(t, err)
	return &sshExecutor{cfg: cfg, dialer: d, health: testHealth()}
}

func TestSSHExecutor(t *testing.T) {
	srv := startSSHServer(t)
	dir := t.TempDir()
	file := filepath.Join(dir, "version")
	hs := versionServer(t, file)
	cfg := SSHConfig{
		Hosts: []string{srv.addr}, User: "deploy", BatchSize: 1,
		Command:     `test "$OPSHUB_VERSION" != app:boom && printf %s "$OPSHUB_VERSION" > ` + file + ` && echo "deployed $OPSHUB_VERSION to $OPSHUB_ENVIRONMENT"`,
		HealthCheck: &HealthCheck{URL: strings.Replace(hs.URL, "127.0.0.1", "{host}", 1), TimeoutSeconds: 1},
	}
	ctx := context.Background()

	// Not pinned: the test reports the fingerprint and sends no credentials.
	x := sshExec(t, srv, cfg)
	res := x.Test(ctx)
	require.Len(t, res.Checks, 1)
	assert.False(t, res.OK)
	assert.Equal(t, srv.fingerprint, res.Checks[0].Fingerprint)
	assert.Empty(t, srv.ran())
	_, err := x.Deploy(ctx, Release{DeploymentID: uuid.New(), Version: "app:v1"}, &bytes.Buffer{})
	assert.Equal(t, ReasonHostKeyUntrusted, reasonOf(err))

	cfg.HostKeys = map[string]string{srv.addr: srv.fingerprint}
	x = sshExec(t, srv, cfg)
	res = x.Test(ctx)
	assert.True(t, res.OK, "%+v", res)
	assert.True(t, res.Checks[0].Pinned)

	var log bytes.Buffer
	out, err := x.Deploy(ctx, Release{DeploymentID: uuid.New(), Version: "app:v1", Environment: "production"}, &log)
	require.NoError(t, err, log.String())
	assert.Contains(t, log.String(), "["+srv.addr+"] deployed app:v1 to production")
	require.Len(t, out.Health, 1)
	assert.True(t, out.Health[0].OK)
	got, _ := os.ReadFile(file) // #nosec G304 -- test file
	assert.Equal(t, "app:v1", string(got))

	// Unhealthy release: the previous one is put back.
	log.Reset()
	out, err = x.Deploy(ctx, Release{DeploymentID: uuid.New(), Version: "app:bad", Previous: "app:v1"}, &log)
	assert.Equal(t, ReasonUnhealthy, reasonOf(err))
	assert.True(t, out.Reverted, log.String())
	got, _ = os.ReadFile(file) // #nosec G304 -- test file
	assert.Equal(t, "app:v1", string(got))

	// The command fails: also reverted.
	out, err = x.Deploy(ctx, Release{DeploymentID: uuid.New(), Version: "app:boom", Previous: "app:v1"}, &bytes.Buffer{})
	assert.Equal(t, ReasonDeployFailed, reasonOf(err))
	assert.True(t, out.Reverted)

	// Without a previous release there is nothing to revert to.
	out, err = x.Deploy(ctx, Release{DeploymentID: uuid.New(), Version: "app:bad"}, &bytes.Buffer{})
	assert.Error(t, err)
	assert.False(t, out.Reverted)

	// A changed host key is refused.
	cfg.HostKeys = map[string]string{srv.addr: "SHA256:" + strings.Repeat("B", 43)}
	x = sshExec(t, srv, cfg)
	_, err = x.Deploy(ctx, Release{DeploymentID: uuid.New(), Version: "app:v2"}, &bytes.Buffer{})
	assert.Equal(t, ReasonHostKeyUntrusted, reasonOf(err))
	res = x.Test(ctx)
	assert.Contains(t, res.Checks[0].Detail, "changed")

	// The environment reaches the command with safe quoting.
	assert.True(t, slices.ContainsFunc(srv.ran(), func(c string) bool {
		return strings.Contains(c, "export OPSHUB_VERSION='app:v1'\nexport") || strings.Contains(c, "export OPSHUB_VERSION='app:v1'\n")
	}))
}

// fakeKube is an in-memory Kubernetes API for deployments and services. Rollouts finish
// immediately unless the image contains "stall".
type fakeKube struct {
	mu          sync.Mutex
	deployments map[string]map[string]any
	selectors   map[string]map[string]string
	calls       []string
}

func (f *fakeKube) deployment(name, image string, replicas int) {
	f.deployments[name] = map[string]any{
		"name": name, "generation": 1, "replicas": replicas, "image": image, "container": "app",
		"labels": map[string]any{"app": "web"},
	}
}

func (f *fakeKube) render(d map[string]any) map[string]any {
	rep := d["replicas"].(int)
	gen := d["generation"].(int)
	status := map[string]any{"observedGeneration": gen, "replicas": rep, "updatedReplicas": rep, "availableReplicas": rep}
	if strings.Contains(d["image"].(string), "stall") {
		status = map[string]any{"observedGeneration": gen, "replicas": rep + 1, "updatedReplicas": 1, "availableReplicas": rep - 1,
			"conditions": []map[string]string{{"type": "Progressing", "status": "False", "reason": "ProgressDeadlineExceeded", "message": "ImagePullBackOff"}}}
	}
	return map[string]any{
		"metadata": map[string]any{"name": d["name"], "generation": gen, "labels": d["labels"]},
		"spec": map[string]any{
			"replicas": rep, "selector": map[string]any{"matchLabels": map[string]any{"app": "web"}},
			"template": map[string]any{
				"metadata": map[string]any{"labels": d["labels"]},
				"spec":     map[string]any{"containers": []map[string]any{{"name": d["container"], "image": d["image"]}}},
			},
		},
		"status": status,
	}
}

func (f *fakeKube) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer t0ken" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	switch {
	case r.URL.Path == "/version":
		_ = json.NewEncoder(w).Encode(map[string]string{"gitVersion": "v1.31.0"})
	case len(parts) == 7 && parts[5] == "deployments" && r.Method == http.MethodGet:
		d, ok := f.deployments[parts[6]]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(f.render(d))
	case len(parts) == 7 && parts[5] == "deployments" && r.Method == http.MethodPatch:
		d, ok := f.deployments[parts[6]]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		spec := body["spec"].(map[string]any)
		if rep, ok := spec["replicas"].(float64); ok {
			d["replicas"] = int(rep)
		}
		c := spec["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
		d["image"] = c["image"]
		d["generation"] = d["generation"].(int) + 1
		_, _ = w.Write([]byte("{}"))
	case len(parts) == 6 && parts[5] == "deployments" && r.Method == http.MethodPost:
		meta := body["metadata"].(map[string]any)
		spec := body["spec"].(map[string]any)
		tmpl := spec["template"].(map[string]any)
		c := tmpl["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
		f.deployments[meta["name"].(string)] = map[string]any{
			"name": meta["name"], "generation": 1, "replicas": int(spec["replicas"].(float64)), "image": c["image"],
			"container": c["name"], "labels": tmpl["metadata"].(map[string]any)["labels"],
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("{}"))
	case len(parts) == 6 && parts[4] == "services" && r.Method == http.MethodGet:
		sel, ok := f.selectors[parts[5]]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"spec": map[string]any{"selector": sel}})
	case len(parts) == 6 && parts[4] == "services" && r.Method == http.MethodPatch:
		sel := f.selectors[parts[5]]
		for k, v := range body["spec"].(map[string]any)["selector"].(map[string]any) {
			if v == nil {
				delete(sel, k)
			} else {
				sel[k] = v.(string)
			}
		}
		_, _ = w.Write([]byte("{}"))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func startFakeKube(t *testing.T) (*fakeKube, string) {
	t.Helper()
	f := &fakeKube{deployments: map[string]map[string]any{}, selectors: map[string]map[string]string{}}
	srv := httptest.NewTLSServer(f)
	t.Cleanup(srv.Close)
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	return f, kubeconfigYAML(srv.URL, ca, "    token: t0ken")
}

func kubeExec(t *testing.T, kubeconfig string, cfg KubernetesConfig) *kubeExecutor {
	t.Helper()
	kc, err := parseKubeconfig(kubeconfig)
	require.NoError(t, err)
	k, err := newKubeClient(kc, loopback.Dialer())
	require.NoError(t, err)
	_, err = cfg.normalize()
	require.NoError(t, err)
	return &kubeExecutor{cfg: cfg, kube: k, health: testHealth()}
}

func TestKubernetesRolling(t *testing.T) {
	f, kc := startFakeKube(t)
	f.deployment("web", "app:v1", 3)
	x := kubeExec(t, kc, KubernetesConfig{Namespace: "prod", Deployment: "web", RolloutTimeoutSeconds: 10})
	ctx := context.Background()

	res := x.Test(ctx)
	assert.True(t, res.OK, "%+v", res)
	assert.Contains(t, res.Checks[0].Detail, "v1.31.0")

	var log bytes.Buffer
	out, err := x.Deploy(ctx, Release{DeploymentID: uuid.New(), Version: "app:v2", Strategy: "rolling"}, &log)
	require.NoError(t, err, log.String())
	assert.Equal(t, "app:v1", out.Previous)
	assert.Equal(t, "app:v2", f.deployments["web"]["image"])
	assert.Contains(t, log.String(), "3 of 3 updated")

	// A stalled rollout is rolled back to the previous image.
	out, err = x.Deploy(ctx, Release{DeploymentID: uuid.New(), Version: "app:stall", Strategy: "rolling"}, &log)
	assert.Equal(t, ReasonUnhealthy, reasonOf(err))
	assert.ErrorContains(t, err, "ImagePullBackOff")
	assert.True(t, out.Reverted)
	assert.Equal(t, "app:v2", f.deployments["web"]["image"])

	x2 := kubeExec(t, kc, KubernetesConfig{Namespace: "prod", Deployment: "missing"})
	_, err = x2.Deploy(ctx, Release{DeploymentID: uuid.New(), Version: "app:v2"}, &log)
	assert.Equal(t, ReasonDeployFailed, reasonOf(err))
}

func TestKubernetesBlueGreen(t *testing.T) {
	f, kc := startFakeKube(t)
	f.deployment("web", "app:v1", 2)
	f.selectors["web"] = map[string]string{"app": "web"}
	x := kubeExec(t, kc, KubernetesConfig{Namespace: "prod", Deployment: "web", Service: "web", RolloutTimeoutSeconds: 10})
	ctx := context.Background()
	var log bytes.Buffer

	// First deploy: web-blue is created from web, and the Service switches to blue.
	out, err := x.Deploy(ctx, Release{DeploymentID: uuid.New(), Version: "app:v2", Strategy: "blue_green"}, &log)
	require.NoError(t, err, log.String())
	assert.Equal(t, "app:v1", out.Previous)
	require.Contains(t, f.deployments, "web-blue")
	assert.Equal(t, "app:v2", f.deployments["web-blue"]["image"])
	assert.Equal(t, 2, f.deployments["web-blue"]["replicas"])
	assert.Equal(t, "blue", f.deployments["web-blue"]["labels"].(map[string]any)[colorLabel])
	assert.Equal(t, "blue", f.selectors["web"][colorLabel])
	assert.Equal(t, "app:v1", f.deployments["web"]["image"], "the original is untouched")

	// Second: green gets v3; blue keeps v2 running.
	out, err = x.Deploy(ctx, Release{DeploymentID: uuid.New(), Version: "app:v3", Strategy: "blue_green"}, &log)
	require.NoError(t, err)
	assert.Equal(t, "app:v2", out.Previous)
	assert.Equal(t, "green", f.selectors["web"][colorLabel])
	assert.Equal(t, "app:v2", f.deployments["web-blue"]["image"])

	// A stalled idle color never receives traffic.
	out, err = x.Deploy(ctx, Release{DeploymentID: uuid.New(), Version: "app:stall", Strategy: "blue_green"}, &log)
	assert.Equal(t, ReasonUnhealthy, reasonOf(err))
	assert.True(t, out.Reverted)
	assert.Equal(t, "green", f.selectors["web"][colorLabel])

	// An unhealthy switch goes back.
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) }))
	defer hs.Close()
	x.cfg.HealthCheck = &HealthCheck{URL: hs.URL, TimeoutSeconds: 1}
	out, err = x.Deploy(ctx, Release{DeploymentID: uuid.New(), Version: "app:v4", Strategy: "blue_green"}, &log)
	assert.Equal(t, ReasonUnhealthy, reasonOf(err))
	assert.True(t, out.Reverted)
	assert.Equal(t, "green", f.selectors["web"][colorLabel])

	// Without a service, blue/green isn't possible.
	x.cfg.Service = ""
	_, err = x.Deploy(ctx, Release{DeploymentID: uuid.New(), Version: "app:v4", Strategy: "blue_green"}, &log)
	assert.Equal(t, ReasonStrategyUnsupport, reasonOf(err))
}
