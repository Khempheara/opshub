package alert

import (
	"context"
	"time"

	"github.com/riverqueue/river"

	"github.com/opshub/opshub/internal/jobs"
)

// Register adds the evaluation worker to a River client.
func (s *Service) Register(w *river.Workers) {
	river.AddWorker(w, &evaluationWorker{svc: s})
}

// Periodic evaluates rules and sends due escalations every 30 seconds.
func Periodic() []*river.PeriodicJob {
	return []*river.PeriodicJob{
		river.NewPeriodicJob(river.PeriodicInterval(EvaluateEvery),
			func() (river.JobArgs, *river.InsertOpts) { return jobs.AlertEvaluationArgs{}, nil },
			&river.PeriodicJobOpts{RunOnStart: true}),
	}
}

type evaluationWorker struct {
	river.WorkerDefaults[jobs.AlertEvaluationArgs]
	svc *Service
}

func (w *evaluationWorker) Work(ctx context.Context, _ *river.Job[jobs.AlertEvaluationArgs]) error {
	if err := w.svc.Evaluate(ctx); err != nil {
		return err
	}
	return w.svc.Escalate(ctx)
}

func (w *evaluationWorker) Timeout(*river.Job[jobs.AlertEvaluationArgs]) time.Duration {
	return 2 * time.Minute
}
