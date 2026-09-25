package infra

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/authz"
	"github.com/opshub/opshub/internal/store"
)

// Retention: raw samples are kept for this month and the previous one (whole monthly
// partitions are dropped); hourly rollups for 400 days.
const (
	RawKeepMonths   = 1
	RawRange        = 30 * 24 * time.Hour // queries within this range use raw samples
	HourlyRetention = 400 * 24 * time.Hour
	MaxPoints       = 1000
	MaxRange        = HourlyRetention
)

// Steps offered for charts; the API picks the smallest giving at most ~300 points.
var steps = []time.Duration{
	30 * time.Second, time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour, 6 * time.Hour, 24 * time.Hour,
}

// Point is one bucket of a series; nil means no data.
type Point struct {
	T       time.Time `json:"t"`
	CPU     *float32  `json:"cpu_pct"`
	CPUMax  *float32  `json:"cpu_max"`
	Mem     *float32  `json:"mem_pct"`
	MemMax  *float32  `json:"mem_max"`
	Disk    *float32  `json:"disk_pct"`
	DiskMax *float32  `json:"disk_max"`
}

// Series is GET /assets/{id}/metrics.
type Series struct {
	From        time.Time `json:"from"`
	To          time.Time `json:"to"`
	StepSeconds int       `json:"step_seconds"`
	Source      string    `json:"source"` // raw | hourly
	Points      []Point   `json:"points"`
}

func val(v float32) *float32 {
	if v < 0 {
		return nil
	}
	return &v
}

// MetricsQuery bounds a series; zero values pick defaults (the last hour, automatic step).
type MetricsQuery struct {
	From, To time.Time
	Step     time.Duration
}

// Metrics returns CPU/RAM/disk averages and maxima per step (infra.view).
func (s *Service) Metrics(ctx context.Context, id uuid.UUID, mq MetricsQuery) (Series, error) {
	q := store.New(s.pool)
	a, err := s.loadAsset(ctx, q, id, authz.InfraView)
	if err != nil {
		return Series{}, err
	}
	now := s.now()
	if mq.To.IsZero() {
		mq.To = now
	}
	if mq.From.IsZero() {
		mq.From = mq.To.Add(-time.Hour)
	}
	span := mq.To.Sub(mq.From)
	var fields []apperr.FieldError
	if span <= 0 {
		fields = append(fields, apperr.FieldError{Field: "from", Rule: "before", Param: "to"})
	}
	if span > MaxRange {
		fields = append(fields, apperr.FieldError{Field: "from", Rule: "max_range", Param: "400d"})
	}
	if mq.Step != 0 && (mq.Step < 10*time.Second || span/mq.Step > MaxPoints) {
		fields = append(fields, apperr.FieldError{Field: "step", Rule: "range", Param: "10s-"})
	}
	if len(fields) > 0 {
		return Series{}, apperr.Validation(fields)
	}
	source := "raw"
	if mq.From.Before(now.Add(-RawRange)) {
		source = "hourly"
	}
	if mq.Step == 0 {
		mq.Step = steps[len(steps)-1]
		for _, st := range steps {
			if span/st <= 300 {
				mq.Step = st
				break
			}
		}
	}
	if source == "hourly" && mq.Step < time.Hour {
		mq.Step = time.Hour
	}
	out := Series{From: mq.From, To: mq.To, StepSeconds: int(mq.Step / time.Second), Source: source, Points: []Point{}}
	stepSeconds := int32(mq.Step / time.Second) // #nosec G115 -- bounded by MaxRange
	if source == "raw" {
		rows, err := q.RawMetricSeries(ctx, store.RawMetricSeriesParams{StepSeconds: stepSeconds, AssetID: a.ID, FromTime: mq.From, ToTime: mq.To})
		if err != nil {
			return Series{}, err
		}
		for _, r := range rows {
			out.Points = append(out.Points, Point{T: r.Bucket, CPU: val(r.CpuAvg), CPUMax: val(r.CpuMax), Mem: val(r.MemAvg),
				MemMax: val(r.MemMax), Disk: val(r.DiskAvg), DiskMax: val(r.DiskMax)})
		}
		return out, nil
	}
	rows, err := q.HourlyMetricSeries(ctx, store.HourlyMetricSeriesParams{StepSeconds: stepSeconds, AssetID: a.ID, FromTime: mq.From, ToTime: mq.To})
	if err != nil {
		return Series{}, err
	}
	for _, r := range rows {
		out.Points = append(out.Points, Point{T: r.Bucket, CPU: val(r.CpuAvg), CPUMax: val(r.CpuMax), Mem: val(r.MemAvg),
			MemMax: val(r.MemMax), Disk: val(r.DiskAvg), DiskMax: val(r.DiskMax)})
	}
	return out, nil
}

// Maintain rolls the last hours of raw samples up into hourly rows, drops expired raw
// partitions (creating upcoming ones) and deletes old rollups. It runs hourly.
func (s *Service) Maintain(ctx context.Context) error {
	q := store.New(s.pool)
	now := s.now().UTC().Truncate(time.Hour)
	// The current hour is still filling; three earlier hours cover a missed run or two.
	if _, err := q.RollupMetrics(ctx, store.RollupMetricsParams{FromTime: now.Add(-3 * time.Hour), ToTime: now}); err != nil {
		return err
	}
	if err := q.MaintainMetricPartitions(ctx, RawKeepMonths); err != nil {
		return err
	}
	_, err := q.DeleteOldHourlyMetrics(ctx, now.Add(-HourlyRetention))
	return err
}
