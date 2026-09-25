package pipeline

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/opshub/opshub/internal/jobs"
)

// Register adds the pipeline workers to a River client (jobs.Deps.Register).
func (s *Service) Register(w *river.Workers) {
	river.AddWorker(w, &eventWorker{svc: s})
	river.AddWorker(w, &scheduleWorker{svc: s})
	river.AddWorker(w, &tickWorker{svc: s})
}

// Periodic is the minute tick (jobs.Deps.Periodic).
func Periodic() []*river.PeriodicJob {
	return []*river.PeriodicJob{
		river.NewPeriodicJob(river.PeriodicInterval(time.Minute),
			func() (river.JobArgs, *river.InsertOpts) {
				return jobs.PipelineTickArgs{}, &river.InsertOpts{Queue: jobs.QueuePipelines}
			},
			&river.PeriodicJobOpts{RunOnStart: true}),
	}
}

type eventWorker struct {
	river.WorkerDefaults[jobs.PipelineFromEventArgs]
	svc *Service
}

func (w *eventWorker) Work(ctx context.Context, job *river.Job[jobs.PipelineFromEventArgs]) error {
	return w.svc.RunFromEvent(ctx, job.Args)
}

func (w *eventWorker) Timeout(*river.Job[jobs.PipelineFromEventArgs]) time.Duration {
	return time.Minute
}

type scheduleWorker struct {
	river.WorkerDefaults[jobs.PipelineScheduleArgs]
	svc *Service
}

func (w *scheduleWorker) Work(ctx context.Context, job *river.Job[jobs.PipelineScheduleArgs]) error {
	return w.svc.RunFromSchedule(ctx, job.Args)
}

func (w *scheduleWorker) Timeout(*river.Job[jobs.PipelineScheduleArgs]) time.Duration {
	return time.Minute
}

type tickWorker struct {
	river.WorkerDefaults[jobs.PipelineTickArgs]
	svc *Service
}

func (w *tickWorker) Work(ctx context.Context, _ *river.Job[jobs.PipelineTickArgs]) error {
	client := river.ClientFromContext[pgx.Tx](ctx)
	return w.svc.Tick(ctx, func(ctx context.Context, tx pgx.Tx, args jobs.PipelineScheduleArgs) error {
		_, err := client.InsertTx(ctx, tx, args, nil)
		return err
	})
}
