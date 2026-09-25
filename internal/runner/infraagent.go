package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Sample is one set of host metrics; nil fields are unavailable on this OS.
type Sample struct {
	CPU           *float32 `json:"cpu_pct"`
	Mem           *float32 `json:"mem_pct"`
	Disk          *float32 `json:"disk_pct"`
	Load          *float32 `json:"load1"`
	UptimeSeconds int64    `json:"uptime_seconds"`
}

// cpuTimes is the aggregate line of /proc/stat.
type cpuTimes struct{ idle, total uint64 }

// parseCPU reads the "cpu" line of /proc/stat (idle includes iowait; steal and guest time
// are part of the total as the kernel reports them).
func parseCPU(stat string) (cpuTimes, error) {
	for _, line := range strings.Split(stat, "\n") {
		f := strings.Fields(line)
		if len(f) < 5 || f[0] != "cpu" {
			continue
		}
		var t cpuTimes
		for i, v := range f[1:] {
			n, err := strconv.ParseUint(v, 10, 64)
			if err != nil {
				return t, fmt.Errorf("/proc/stat: %w", err)
			}
			if i >= 8 { // guest and guest_nice are already counted in user and nice
				break
			}
			t.total += n
			if i == 3 || i == 4 { // idle, iowait
				t.idle += n
			}
		}
		return t, nil
	}
	return cpuTimes{}, errors.New("/proc/stat: no cpu line")
}

// cpuPercent is the busy share between two readings.
func cpuPercent(a, b cpuTimes) *float32 {
	if b.total <= a.total {
		return nil
	}
	busy := float64((b.total-a.total)-(b.idle-a.idle)) / float64(b.total-a.total) * 100
	return pct(busy)
}

// parseMem computes used memory from /proc/meminfo (MemTotal − MemAvailable).
func parseMem(meminfo string) *float32 {
	var total, avail float64
	for _, line := range strings.Split(meminfo, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		v, err := strconv.ParseFloat(f[1], 64)
		if err != nil {
			continue
		}
		switch f[0] {
		case "MemTotal:":
			total = v
		case "MemAvailable:":
			avail = v
		}
	}
	if total <= 0 {
		return nil
	}
	return pct((total - avail) / total * 100)
}

// parseFirstFloat reads the first number of /proc/loadavg or /proc/uptime.
func parseFirstFloat(s string) (float64, bool) {
	f := strings.Fields(s)
	if len(f) == 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(f[0], 64)
	return v, err == nil
}

// diskPercent is used / (used + available to unprivileged users), like df.
func diskPercent(blocks, free, avail uint64) *float32 {
	used := blocks - free
	if used+avail == 0 {
		return nil
	}
	return pct(float64(used) / float64(used+avail) * 100)
}

func pct(v float64) *float32 {
	if v < 0 {
		v = 0
	}
	if v > 100 {
		v = 100
	}
	f := float32(v)
	return &f
}

// InfraAgent sends host metrics to OpsHub for one server asset.
type InfraAgent struct {
	URL       string
	Token     string
	DiskPath  string // filesystem to report (default "/")
	ProcRoot  string // "/proc", or the host's /proc mounted into a container
	Hostname  string // reported name (default: this machine's)
	Version   string
	Logger    *slog.Logger
	collector func(ctx context.Context) Sample
}

// ErrAgentToken means OpsHub rejected the token (rotated or the asset was deleted).
var ErrAgentToken = errors.New("the agent token was rejected (rotated, or the asset was deleted)")

// Run reports until ctx ends. The first sample waits one second to measure CPU.
func (a *InfraAgent) Run(ctx context.Context) error {
	if a.collector == nil {
		a.collector = a.collect
	}
	if a.DiskPath == "" {
		a.DiskPath = "/"
	}
	if a.ProcRoot == "" {
		a.ProcRoot = "/proc"
	}
	client := NewClient(a.URL, a.Version)
	hostname := a.Hostname
	if hostname == "" {
		hostname, _ = os.Hostname()
	}
	interval := 30 * time.Second
	backoff := time.Second
	a.Logger.Info("infra agent started", "url", a.URL, "disk", a.DiskPath, "os", runtime.GOOS)
	for {
		s := a.collector(ctx)
		if ctx.Err() != nil {
			return nil
		}
		next, err := client.InfraHeartbeat(ctx, a.Token, map[string]any{
			"version": a.Version, "hostname": hostname, "os": runtime.GOOS, "arch": runtime.GOARCH,
			"uptime_seconds": s.UptimeSeconds, "cpu_pct": s.CPU, "mem_pct": s.Mem, "disk_pct": s.Disk, "load1": s.Load,
		})
		switch {
		case IsCode(err, "AGENT_TOKEN_INVALID"):
			return ErrAgentToken
		case err != nil:
			a.Logger.Warn("heartbeat failed", "error", err, "retry_in", backoff)
			sleep(ctx, backoff)
			backoff = min(backoff*2, 2*time.Minute)
			continue
		}
		backoff = time.Second
		if next > 0 {
			interval = time.Duration(next) * time.Second
		}
		// The collector already spent a second measuring CPU.
		sleep(ctx, max(interval-time.Second, time.Second))
		if ctx.Err() != nil {
			return nil
		}
	}
}

// readFile reads a /proc file.
func readFile(path string) (string, error) {
	b, err := os.ReadFile(path) // #nosec G304 -- fixed /proc paths
	return string(b), err
}
