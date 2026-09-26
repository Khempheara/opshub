package notify

import (
	"context"
	"time"

	"github.com/riverqueue/river"

	"github.com/opshub/opshub/internal/jobs"
)

// Register adds the delivery worker to a River client.
func (s *Service) Register(w *river.Workers) {
	river.AddWorker(w, &notifyWorker{svc: s})
}

type notifyWorker struct {
	river.WorkerDefaults[jobs.NotifyArgs]
	svc *Service
}

func (w *notifyWorker) Work(ctx context.Context, job *river.Job[jobs.NotifyArgs]) error {
	return w.svc.Deliver(ctx, job.Args, job.Attempt, job.MaxAttempts)
}

func (w *notifyWorker) Timeout(*river.Job[jobs.NotifyArgs]) time.Duration { return time.Minute }
