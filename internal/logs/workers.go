package logs

import (
	"context"
	"time"

	"github.com/riverqueue/river"

	"github.com/opshub/opshub/internal/jobs"
)

// Register adds the partition maintenance worker to a River client.
func (s *Service) Register(w *river.Workers) {
	river.AddWorker(w, &maintenanceWorker{svc: s})
}

// Periodic schedules partition maintenance hourly.
func Periodic() []*river.PeriodicJob {
	return []*river.PeriodicJob{
		river.NewPeriodicJob(river.PeriodicInterval(time.Hour),
			func() (river.JobArgs, *river.InsertOpts) { return jobs.LogMaintenanceArgs{}, nil },
			&river.PeriodicJobOpts{RunOnStart: true}),
	}
}

type maintenanceWorker struct {
	river.WorkerDefaults[jobs.LogMaintenanceArgs]
	svc *Service
}

func (w *maintenanceWorker) Work(ctx context.Context, _ *river.Job[jobs.LogMaintenanceArgs]) error {
	return w.svc.Maintain(ctx)
}
