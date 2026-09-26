package deploy

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/opshub/opshub/internal/dockerapi"
)

// fakeDocker answers the calls revert makes; start fails `busy` times with the error Docker
// gives while a removed container's ports are still being released.
type fakeDocker struct {
	mu     sync.Mutex
	busy   int
	starts int
	calls  []string
}

func (f *fakeDocker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := r.URL.Path
	if i := strings.Index(path, "/containers/"); i > 0 {
		path = path[i:] // drop the /v1.xx prefix
	}
	f.calls = append(f.calls, r.Method+" "+path)
	switch {
	case strings.HasSuffix(path, "/_ping"):
		w.Header().Set("Api-Version", "1.45")
		w.WriteHeader(http.StatusOK)
		return
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/start"):
		f.starts++
		if f.starts <= f.busy {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"driver failed programming external connectivity: Bind for 127.0.0.1:8080 failed: port is already allocated"}`))
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func fakeExecutor(t *testing.T, f *fakeDocker) *dockerExecutor {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	addr := strings.TrimPrefix(srv.URL, "http://")
	dc, err := dockerapi.New(context.Background(), func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", addr)
	}, nil)
	require.NoError(t, err)
	return &dockerExecutor{docker: dc}
}

func TestRevertRetriesStartWhilePortsAreReleased(t *testing.T) {
	old := restartDelay
	restartDelay = time.Millisecond
	t.Cleanup(func() { restartDelay = old })

	f := &fakeDocker{busy: 2}
	x := fakeExecutor(t, f)
	var log bytes.Buffer
	assert.True(t, x.revert(context.Background(), []replaced{{name: "web", hadPrevious: true}}, &log), log.String())
	assert.Equal(t, 3, f.starts)
	assert.Contains(t, log.String(), "Restored the previous web")
	assert.Contains(t, f.calls, "DELETE /containers/web")
	assert.Contains(t, f.calls, "POST /containers/web-previous/rename")

	// When it never starts, the reason is in the deployment log.
	f = &fakeDocker{busy: restartAttempts}
	x = fakeExecutor(t, f)
	log.Reset()
	assert.False(t, x.revert(context.Background(), []replaced{{name: "web", hadPrevious: true}}, &log))
	assert.Equal(t, restartAttempts, f.starts)
	assert.Contains(t, log.String(), "Couldn't start the previous web")
	assert.Contains(t, log.String(), "port is already allocated")
}
