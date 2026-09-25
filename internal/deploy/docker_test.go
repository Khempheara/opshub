package deploy

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/opshub/opshub/internal/dockerapi"
	"github.com/opshub/opshub/internal/safehttp"
)

// dockerSocket finds a local Docker daemon, or skips the test.
func dockerSocket(t *testing.T) string {
	t.Helper()
	if os.Getenv("OPSHUB_SKIP_DOCKER_TESTS") != "" {
		t.Skip("OPSHUB_SKIP_DOCKER_TESTS is set")
	}
	candidates := []string{strings.TrimPrefix(os.Getenv("DOCKER_HOST"), "unix://"), "/var/run/docker.sock"}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".docker/run/docker.sock"))
	}
	for _, c := range candidates {
		if c != "" && !strings.Contains(c, "://") {
			if _, err := os.Stat(c); err == nil {
				return c
			}
		}
	}
	t.Skip("no Docker socket")
	return ""
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()
	return ln.Addr().(*net.TCPAddr).Port
}

// TestDockerExecutor replaces a container with real Docker: a good release, then one that
// exits at once (the previous container comes back), then a redeploy.
func TestDockerExecutor(t *testing.T) {
	socket := dockerSocket(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	port := freePort(t)
	name := "opshub-test-" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	cfg := DockerConfig{
		Connection: DockerViaLocal, Socket: socket, Container: name, Ports: []string{fmt.Sprintf("127.0.0.1:%d:80", port)},
		Env:         map[string]string{"GREETING": "hello"},
		HealthCheck: &HealthCheck{URL: fmt.Sprintf("http://127.0.0.1:%d/", port), TimeoutSeconds: 30},
	}
	_, err := cfg.normalize(true)
	require.NoError(t, err)
	opts := safehttp.Options{AllowedCIDRs: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}}
	dc, sc, err := connectDocker(ctx, cfg, DockerCredentials{}, opts.Dialer())
	require.NoError(t, err)
	assert.Nil(t, sc)
	x := &dockerExecutor{cfg: cfg, docker: dc, health: testHealth()}
	t.Cleanup(func() {
		_ = dc.RemoveContainer(context.Background(), name)
		_ = dc.RemoveContainer(context.Background(), name+"-previous")
	})

	res := x.Test(ctx)
	require.True(t, res.OK, "%+v", res)
	assert.Contains(t, res.Checks[0].Detail, "Docker ")
	assert.Equal(t, "not created yet", res.Checks[1].Detail)

	var log bytes.Buffer
	good := "nginx:1.27-alpine"
	out, err := x.Deploy(ctx, Release{DeploymentID: uuid.New(), Version: good, Environment: "staging", Project: "api"}, &log)
	require.NoError(t, err, log.String())
	require.NotEmpty(t, out.Health)
	assert.True(t, out.Health[len(out.Health)-1].OK)
	info, err := dc.InspectContainer(ctx, name)
	require.NoError(t, err)
	assert.True(t, info.Running)
	assert.Equal(t, good, info.Image)

	// busybox exits immediately: the release fails and the nginx container is restored.
	log.Reset()
	out, err = x.Deploy(ctx, Release{DeploymentID: uuid.New(), Version: "busybox:1.36", Previous: good}, &log)
	require.Error(t, err, log.String())
	assert.Equal(t, ReasonUnhealthy, reasonOf(err))
	assert.True(t, out.Reverted, log.String())
	assert.Equal(t, good, out.Previous)
	info, err = dc.InspectContainer(ctx, name)
	require.NoError(t, err)
	assert.True(t, info.Running)
	assert.Equal(t, good, info.Image)
	_, err = dc.InspectContainer(ctx, name+"-previous")
	assert.ErrorIs(t, err, dockerapi.ErrNotFound)
	h := testHealth().check(ctx, *cfg.HealthCheck, "", &bytes.Buffer{})
	assert.True(t, h.OK, "the restored container serves again")

	// A redeploy replaces it and removes the stopped previous container.
	out, err = x.Deploy(ctx, Release{DeploymentID: uuid.New(), Version: good}, &log)
	require.NoError(t, err, log.String())
	assert.False(t, out.Reverted)
	_, err = dc.InspectContainer(ctx, name+"-previous")
	assert.ErrorIs(t, err, dockerapi.ErrNotFound)
}
