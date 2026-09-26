package monitor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/opshub/opshub/internal/safehttp"
	"github.com/opshub/opshub/internal/store"
	"github.com/opshub/opshub/internal/tlsprobe"
)

// Check scheduling.
const (
	claimBatch       = 200
	checkConcurrency = 20
	maxBodyBytes     = 1 << 20 // read at most 1 MiB when looking for a keyword
)

// Result is one check's outcome.
type Result struct {
	Up         bool
	LatencyMs  *int32
	StatusCode *int32
	Error      string
}

func ms(d time.Duration) *int32 {
	v := int32(min(d.Milliseconds(), 1<<30)) // #nosec G115 -- bounded
	return &v
}

func dialErr(err error) string {
	if safehttp.IsBlocked(err) {
		return "address not allowed (OPSHUB_OUTBOUND_ALLOWED_CIDRS)"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timed out"
	}
	return "connection failed: " + tlsprobe.ShortErr(err)
}

// Run performs one check of m now.
func (s *Service) Run(ctx context.Context, m store.Monitor) Result {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(m.TimeoutMs)*time.Millisecond)
	defer cancel()
	var set Settings
	_ = json.Unmarshal(m.Config, &set)
	start := time.Now()
	switch m.Kind {
	case store.MonitorKindHttp:
		return s.checkHTTP(ctx, m.Target, set, start)
	case store.MonitorKindTcp:
		conn, err := s.dialer.DialContext(ctx, "tcp", m.Target)
		if err != nil {
			return Result{Error: dialErr(err)}
		}
		_ = conn.Close()
		return Result{Up: true, LatencyMs: ms(time.Since(start))}
	case store.MonitorKindSsl:
		host, _, _ := net.SplitHostPort(m.Target)
		res := tlsprobe.Probe(ctx, s.dialer, m.Target, host, s.cfg.Roots, s.now())
		latency := ms(time.Since(start))
		if res.Leaf == nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return Result{Error: "timed out"}
			}
			return Result{Error: res.Err}
		}
		if res.Err != "" {
			return Result{LatencyMs: latency, Error: res.Err}
		}
		days := int(res.Leaf.NotAfter.Sub(s.now()).Hours() / 24)
		if days < set.ExpiryDays {
			return Result{LatencyMs: latency, Error: fmt.Sprintf("certificate expires in %d days", days)}
		}
		return Result{Up: true, LatencyMs: latency}
	}
	return Result{Error: "unknown monitor kind"}
}

func (s *Service) checkHTTP(ctx context.Context, target string, set Settings, start time.Time) Result {
	req, err := http.NewRequestWithContext(ctx, set.Method, target, http.NoBody)
	if err != nil {
		return Result{Error: "invalid URL"}
	}
	req.Header.Set("User-Agent", "OpsHub-Monitor")
	resp, err := s.client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return Result{Error: dialErr(err)}
	}
	defer func() { _ = resp.Body.Close() }()
	var body []byte
	if set.Keyword != "" {
		body, err = io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
		if err != nil {
			return Result{Error: dialErr(err)}
		}
	}
	latency := ms(time.Since(start))
	code := int32(resp.StatusCode) // #nosec G115 -- HTTP status codes are small
	res := Result{LatencyMs: latency, StatusCode: &code}
	ok := resp.StatusCode >= 200 && resp.StatusCode <= 399
	if len(set.ExpectedStatus) > 0 {
		ok = slices.Contains(set.ExpectedStatus, resp.StatusCode)
	}
	switch {
	case !ok:
		res.Error = "unexpected status " + strconv.Itoa(resp.StatusCode)
	case set.Keyword != "" && !bytes.Contains(body, []byte(set.Keyword)):
		res.Error = "keyword not found"
	default:
		res.Up = true
	}
	return res
}

// record stores a result and updates the monitor's state.
func (s *Service) record(ctx context.Context, m store.Monitor, r Result) error {
	return s.inTx(ctx, func(q *store.Queries) error {
		if err := q.InsertMonitorResult(ctx, store.InsertMonitorResultParams{
			MonitorID: m.ID, Up: r.Up, LatencyMs: r.LatencyMs, StatusCode: r.StatusCode, Error: r.Error,
		}); err != nil {
			return err
		}
		return q.SetMonitorState(ctx, store.SetMonitorStateParams{ID: m.ID, Up: &r.Up, LatencyMs: r.LatencyMs, Error: r.Error})
	})
}

// CheckDue runs every due check (claimed so that parallel workers never repeat one) and
// records the results. It returns the number of checks run.
func (s *Service) CheckDue(ctx context.Context) (int, error) {
	due, err := store.New(s.pool).ClaimDueMonitors(ctx, claimBatch)
	if err != nil {
		return 0, err
	}
	sem := make(chan struct{}, checkConcurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	for _, m := range due {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if err := s.record(ctx, m, s.Run(ctx, m)); err != nil {
				s.logger.ErrorContext(ctx, "recording monitor result failed", "monitor_id", m.ID, "error", err)
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return len(due), firstErr
}
