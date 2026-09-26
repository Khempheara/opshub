package monitor

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/authz"
	"github.com/opshub/opshub/internal/store"
)

// Retention: raw results for this month and the previous one; hourly rollups for 400 days.
const (
	RawKeepMonths   = 1
	RawRange        = 30 * 24 * time.Hour // queries within this range use raw results
	HourlyRetention = 400 * 24 * time.Hour
	MaxPoints       = 1000
	RecentChecks    = 20
)

// Steps offered for charts; the API picks the smallest giving at most ~300 points.
var steps = []time.Duration{
	time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour, 6 * time.Hour, 24 * time.Hour,
}

// Point is one bucket: how many checks ran and succeeded, and the latency of successful ones.
type Point struct {
	T          time.Time `json:"t"`
	Checks     int32     `json:"checks"`
	UpChecks   int32     `json:"up_checks"`
	LatencyAvg *float32  `json:"latency_avg"`
	LatencyMax *int32    `json:"latency_max"`
}

// Check is one recent result.
type Check struct {
	At         time.Time `json:"at"`
	Up         bool      `json:"up"`
	LatencyMs  *int32    `json:"latency_ms"`
	StatusCode *int32    `json:"status_code"`
	Error      string    `json:"error"`
}

// Results is GET /monitors/{id}/results.
type Results struct {
	From        time.Time `json:"from"`
	To          time.Time `json:"to"`
	StepSeconds int       `json:"step_seconds"`
	Source      string    `json:"source"` // raw | hourly
	// Uptime is the share of successful checks over the range (null: no checks).
	Uptime *float64 `json:"uptime"`
	Points []Point  `json:"points"`
	Recent []Check  `json:"recent"`
}

func latencyAvg(v float32) *float32 {
	if v < 0 {
		return nil
	}
	return &v
}

func latencyMax(v int32) *int32 {
	if v < 0 {
		return nil
	}
	return &v
}

// ResultsQuery bounds a series; zero values pick defaults (the last 24 hours, automatic step).
type ResultsQuery struct {
	From, To time.Time
	Step     time.Duration
}

// GetResults returns uptime and latency per step plus the latest checks (monitor.view).
func (s *Service) GetResults(ctx context.Context, id uuid.UUID, rq ResultsQuery) (Results, error) {
	q := store.New(s.pool)
	m, _, err := s.load(ctx, q, id, authz.MonitorView)
	if err != nil {
		return Results{}, err
	}
	now := s.now()
	if rq.To.IsZero() {
		rq.To = now
	}
	if rq.From.IsZero() {
		rq.From = rq.To.Add(-24 * time.Hour)
	}
	span := rq.To.Sub(rq.From)
	var fields []apperr.FieldError
	if span <= 0 {
		fields = append(fields, apperr.FieldError{Field: "from", Rule: "before", Param: "to"})
	}
	if span > HourlyRetention {
		fields = append(fields, apperr.FieldError{Field: "from", Rule: "max_range", Param: "400d"})
	}
	if rq.Step != 0 && (rq.Step < time.Minute || span/rq.Step > MaxPoints) {
		fields = append(fields, apperr.FieldError{Field: "step", Rule: "range", Param: "1m-"})
	}
	if len(fields) > 0 {
		return Results{}, apperr.Validation(fields)
	}
	source := "raw"
	if rq.From.Before(now.Add(-RawRange)) {
		source = "hourly"
	}
	if rq.Step == 0 {
		rq.Step = steps[len(steps)-1]
		for _, st := range steps {
			if span/st <= 300 {
				rq.Step = st
				break
			}
		}
	}
	if source == "hourly" && rq.Step < time.Hour {
		rq.Step = time.Hour
	}
	out := Results{From: rq.From, To: rq.To, StepSeconds: int(rq.Step / time.Second), Source: source, Points: []Point{}, Recent: []Check{}}
	step := int32(rq.Step / time.Second) // #nosec G115 -- bounded by HourlyRetention
	var checks, up int64
	if source == "raw" {
		rows, err := q.RawMonitorSeries(ctx, store.RawMonitorSeriesParams{StepSeconds: step, FromTs: rq.From, MonitorID: m.ID, ToTs: rq.To})
		if err != nil {
			return Results{}, err
		}
		for _, r := range rows {
			out.Points = append(out.Points, Point{T: r.Bucket, Checks: r.Checks, UpChecks: r.UpChecks, LatencyAvg: latencyAvg(r.LatencyAvg), LatencyMax: latencyMax(r.LatencyMax)})
			checks, up = checks+int64(r.Checks), up+int64(r.UpChecks)
		}
	} else {
		rows, err := q.HourlyMonitorSeries(ctx, store.HourlyMonitorSeriesParams{StepSeconds: step, FromTs: rq.From, MonitorID: m.ID, ToTs: rq.To})
		if err != nil {
			return Results{}, err
		}
		for _, r := range rows {
			out.Points = append(out.Points, Point{T: r.Bucket, Checks: r.Checks, UpChecks: r.UpChecks, LatencyAvg: latencyAvg(r.LatencyAvg), LatencyMax: latencyMax(r.LatencyMax)})
			checks, up = checks+int64(r.Checks), up+int64(r.UpChecks)
		}
	}
	if checks > 0 {
		v := float64(up) / float64(checks) * 100
		out.Uptime = &v
	}
	recent, err := q.RecentMonitorResults(ctx, store.RecentMonitorResultsParams{MonitorID: m.ID, MaxResults: RecentChecks})
	if err != nil {
		return Results{}, err
	}
	for _, r := range recent {
		out.Recent = append(out.Recent, Check{At: r.Ts, Up: r.Up, LatencyMs: r.LatencyMs, StatusCode: r.StatusCode, Error: r.Error})
	}
	return out, nil
}

// Maintain rolls the last hours up, manages partitions and deletes old rollups (hourly).
func (s *Service) Maintain(ctx context.Context) error {
	q := store.New(s.pool)
	now := s.now().UTC().Truncate(time.Hour)
	// The current hour is still filling; three earlier hours cover a missed run or two.
	if err := q.RollupMonitorResults(ctx, store.RollupMonitorResultsParams{FromTs: now.Add(-3 * time.Hour), ToTs: now}); err != nil {
		return err
	}
	if err := q.MaintainMonitorPartitions(ctx, RawKeepMonths); err != nil {
		return err
	}
	_, err := q.DeleteOldMonitorHourly(ctx, now.Add(-HourlyRetention))
	return err
}
