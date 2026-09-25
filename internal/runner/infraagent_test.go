package runner

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const procStat1 = `cpu  100 0 100 700 100 0 0 0 0 0
cpu0 50 0 50 350 50 0 0 0 0 0
intr 1234
`

// 800 more jiffies, of which 250 idle+iowait: (800-250)/800 = 68.75 % busy.
const procStat2 = `cpu  400 0 300 900 150 0 50 0 0 0
cpu0 200 0 150 450 75 0 25 0 0 0
`

const meminfo = `MemTotal:       16000000 kB
MemFree:         1000000 kB
MemAvailable:    4000000 kB
Buffers:          500000 kB
`

func TestParseCPU(t *testing.T) {
	a, err := parseCPU(procStat1)
	require.NoError(t, err)
	assert.Equal(t, cpuTimes{idle: 800, total: 1000}, a)
	b, err := parseCPU(procStat2)
	require.NoError(t, err)
	got := cpuPercent(a, b)
	require.NotNil(t, got)
	assert.InDelta(t, 68.75, *got, 0.01)
	assert.Nil(t, cpuPercent(b, a), "counters never go backwards")
	_, err = parseCPU("intr 1\n")
	assert.Error(t, err)
	_, err = parseCPU("cpu 1 2 x 4 5\n")
	assert.Error(t, err)
}

func TestParseMemDiskLoad(t *testing.T) {
	m := parseMem(meminfo)
	require.NotNil(t, m)
	assert.InDelta(t, 75, *m, 0.01)
	assert.Nil(t, parseMem("Bogus: 1\n"))

	d := diskPercent(1000, 400, 300) // 600 used, 300 available: 66.7 %
	require.NotNil(t, d)
	assert.InDelta(t, 66.67, *d, 0.01)
	assert.Nil(t, diskPercent(0, 0, 0))

	v, ok := parseFirstFloat("0.52 0.58 0.59 1/467 12345\n")
	assert.True(t, ok)
	assert.InDelta(t, 0.52, v, 0.001)
	_, ok = parseFirstFloat("")
	assert.False(t, ok)
	assert.Equal(t, float32(100), *pct(140))
	assert.Equal(t, float32(0), *pct(-3))
}

func TestInfraAgentReports(t *testing.T) {
	var mu sync.Mutex
	var bodies []map[string]any
	rejected := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		assert.Equal(t, "/api/v1/agent/heartbeat", r.URL.Path)
		assert.Equal(t, "Bearer ohi_test_secret", r.Header.Get("Authorization"))
		if rejected {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"code":"AGENT_TOKEN_INVALID","message":"x","details":{}}}`)
			return
		}
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		bodies = append(bodies, b)
		if len(bodies) == 2 {
			rejected = true // the token is rotated after two reports
		}
		_, _ = io.WriteString(w, `{"interval_seconds":1}`)
	}))
	defer srv.Close()

	cpu := float32(12.5)
	a := &InfraAgent{
		URL: srv.URL, Token: "ohi_test_secret", Version: "1.2.3", Hostname: "web-1",
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		collector: func(context.Context) Sample { return Sample{CPU: &cpu, UptimeSeconds: 3600} },
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := a.Run(ctx)
	assert.ErrorIs(t, err, ErrAgentToken)
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, bodies, 2)
	assert.Equal(t, "web-1", bodies[0]["hostname"])
	assert.Equal(t, "1.2.3", bodies[0]["version"])
	assert.InDelta(t, 12.5, bodies[0]["cpu_pct"], 0.01)
	assert.Nil(t, bodies[0]["mem_pct"], "unknown values are sent as null")
	assert.InDelta(t, 3600, bodies[0]["uptime_seconds"], 0.1)
}
