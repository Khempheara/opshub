//go:build linux

package runner

import (
	"context"
	"syscall"
	"time"
)

// collect reads CPU (over one second), memory, disk, load and uptime from procfs.
func (a *InfraAgent) collect(ctx context.Context) Sample {
	var s Sample
	first, err1 := readFile(a.ProcRoot + "/stat")
	select {
	case <-ctx.Done():
		return s
	case <-time.After(time.Second):
	}
	second, err2 := readFile(a.ProcRoot + "/stat")
	if err1 == nil && err2 == nil {
		t1, e1 := parseCPU(first)
		t2, e2 := parseCPU(second)
		if e1 == nil && e2 == nil {
			s.CPU = cpuPercent(t1, t2)
		}
	}
	if mi, err := readFile(a.ProcRoot + "/meminfo"); err == nil {
		s.Mem = parseMem(mi)
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(a.DiskPath, &st); err == nil {
		s.Disk = diskPercent(st.Blocks, st.Bfree, st.Bavail)
	}
	if la, err := readFile(a.ProcRoot + "/loadavg"); err == nil {
		if v, ok := parseFirstFloat(la); ok {
			f := float32(v)
			s.Load = &f
		}
	}
	if up, err := readFile(a.ProcRoot + "/uptime"); err == nil {
		if v, ok := parseFirstFloat(up); ok {
			s.UptimeSeconds = int64(v)
		}
	}
	return s
}
