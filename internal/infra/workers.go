package infra

import (
	"context"
	"time"

	"github.com/riverqueue/river"

	"github.com/opshub/opshub/internal/jobs"
)

// Register adds the maintenance and certificate workers to a River client.
func (s *Service) Register(w *river.Workers) {
	river.AddWorker(w, &maintenanceWorker{svc: s})
	river.AddWorker(w, &certWorker{svc: s})
}

// Periodic schedules maintenance hourly and certificate checks every 15 minutes (each check
// is due daily, or hourly after a failure).
func Periodic() []*river.PeriodicJob {
	return []*river.PeriodicJob{
		river.NewPeriodicJob(river.PeriodicInterval(time.Hour),
			func() (river.JobArgs, *river.InsertOpts) { return jobs.InfraMaintenanceArgs{}, nil },
			&river.PeriodicJobOpts{RunOnStart: true}),
		river.NewPeriodicJob(river.PeriodicInterval(15*time.Minute),
			func() (river.JobArgs, *river.InsertOpts) { return jobs.CertificateChecksArgs{}, nil },
			&river.PeriodicJobOpts{RunOnStart: true}),
	}
}

type maintenanceWorker struct {
	river.WorkerDefaults[jobs.InfraMaintenanceArgs]
	svc *Service
}

func (w *maintenanceWorker) Work(ctx context.Context, _ *river.Job[jobs.InfraMaintenanceArgs]) error {
	return w.svc.Maintain(ctx)
}

type certWorker struct {
	river.WorkerDefaults[jobs.CertificateChecksArgs]
	svc *Service
}

func (w *certWorker) Work(ctx context.Context, _ *river.Job[jobs.CertificateChecksArgs]) error {
	return w.svc.CheckDue(ctx)
}

func (w *certWorker) Timeout(*river.Job[jobs.CertificateChecksArgs]) time.Duration {
	return 10 * time.Minute
}
