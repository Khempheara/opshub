package monitor

import (
	"context"
	"time"

	"github.com/riverqueue/river"

	"github.com/opshub/opshub/internal/jobs"
)

// CheckEvery is how often due checks are picked up (monitor intervals are 30 s or more).
const CheckEvery = 15 * time.Second

// Register adds the check and maintenance workers to a River client.
func (s *Service) Register(w *river.Workers) {
	river.AddWorker(w, &checksWorker{svc: s})
	river.AddWorker(w, &maintenanceWorker{svc: s})
}

// Periodic schedules checks every 15 seconds and maintenance hourly.
func Periodic() []*river.PeriodicJob {
	return []*river.PeriodicJob{
		river.NewPeriodicJob(river.PeriodicInterval(CheckEvery),
			func() (river.JobArgs, *river.InsertOpts) { return jobs.MonitorChecksArgs{}, nil },
			&river.PeriodicJobOpts{RunOnStart: true}),
		river.NewPeriodicJob(river.PeriodicInterval(time.Hour),
			func() (river.JobArgs, *river.InsertOpts) { return jobs.MonitoringMaintenanceArgs{}, nil },
			&river.PeriodicJobOpts{RunOnStart: true}),
	}
}

type checksWorker struct {
	river.WorkerDefaults[jobs.MonitorChecksArgs]
	svc *Service
}

func (w *checksWorker) Work(ctx context.Context, _ *river.Job[jobs.MonitorChecksArgs]) error {
	_, err := w.svc.CheckDue(ctx)
	return err
}

// A batch of checks each bounded by 30 s, run 20 at a time.
func (w *checksWorker) Timeout(*river.Job[jobs.MonitorChecksArgs]) time.Duration {
	return 10 * time.Minute
}

type maintenanceWorker struct {
	river.WorkerDefaults[jobs.MonitoringMaintenanceArgs]
	svc *Service
}

func (w *maintenanceWorker) Work(ctx context.Context, _ *river.Job[jobs.MonitoringMaintenanceArgs]) error {
	return w.svc.Maintain(ctx)
}
