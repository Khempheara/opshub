package runners

import (
	"context"
	"time"

	"github.com/riverqueue/river"

	"github.com/opshub/opshub/internal/jobs"
)

// Register adds the housekeeping worker to a River client.
func (s *Service) Register(w *river.Workers) {
	river.AddWorker(w, &housekeepingWorker{svc: s})
}

// Periodic schedules housekeeping every 30 seconds, so a lost runner's jobs fail within
// about a minute.
func Periodic() []*river.PeriodicJob {
	return []*river.PeriodicJob{
		river.NewPeriodicJob(river.PeriodicInterval(30*time.Second),
			func() (river.JobArgs, *river.InsertOpts) {
				return jobs.RunnerHousekeepingArgs{}, &river.InsertOpts{Queue: jobs.QueuePipelines}
			},
			&river.PeriodicJobOpts{RunOnStart: true}),
	}
}

type housekeepingWorker struct {
	river.WorkerDefaults[jobs.RunnerHousekeepingArgs]
	svc *Service
}

func (w *housekeepingWorker) Work(ctx context.Context, _ *river.Job[jobs.RunnerHousekeepingArgs]) error {
	return w.svc.Housekeep(ctx)
}

func (w *housekeepingWorker) Timeout(*river.Job[jobs.RunnerHousekeepingArgs]) time.Duration {
	return 5 * time.Minute
}
