package deploy

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"

	"github.com/opshub/opshub/internal/authz"
	"github.com/opshub/opshub/internal/jobs"
	"github.com/opshub/opshub/internal/store"
)

// Register adds the deployment worker to a River client.
func (s *Service) Register(w *river.Workers) {
	river.AddWorker(w, &deployWorker{svc: s})
}

type deployWorker struct {
	river.WorkerDefaults[jobs.DeploymentArgs]
	svc *Service
}

func (w *deployWorker) Work(ctx context.Context, job *river.Job[jobs.DeploymentArgs]) error {
	return w.svc.Run(ctx, job.Args.DeploymentID, job.Attempt)
}

func (w *deployWorker) Timeout(*river.Job[jobs.DeploymentArgs]) time.Duration {
	return deployTimeout + 5*time.Minute
}

// LogChunk is a piece of deployment output.
type LogChunk struct {
	Seq     int32  `json:"seq"`
	Content string `json:"content"`
}

// logPage returns chunks after afterSeq and whether the deployment has finished (read
// first, so that a finished deployment's chunks are final).
func (s *Service) logPage(ctx context.Context, q *store.Queries, id uuid.UUID, afterSeq int32) ([]LogChunk, bool, store.DeploymentStatus, error) {
	d, err := q.GetDeployment(ctx, id)
	if err != nil {
		return nil, false, "", err
	}
	rows, err := q.ListDeploymentLogs(ctx, store.ListDeploymentLogsParams{DeploymentID: id, AfterSeq: afterSeq, PageSize: 500})
	if err != nil {
		return nil, false, "", err
	}
	out := make([]LogChunk, 0, len(rows))
	for _, r := range rows {
		out = append(out, LogChunk{Seq: r.Seq, Content: r.Content})
	}
	finished := d.Status == store.DeploymentStatusSucceeded || d.Status == store.DeploymentStatusFailed
	return out, finished && len(rows) < 500, d.Status, nil
}

// authorizeView checks deployment.view for streams.
func (s *Service) authorizeView(ctx context.Context, id uuid.UUID) error {
	_, _, err := s.loadDeployment(ctx, store.New(s.pool), id, authz.DeploymentView)
	return err
}
