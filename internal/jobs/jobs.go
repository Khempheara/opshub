// Package jobs wires the River background job queue: job argument types, workers, periodic
// jobs and schema migrations. Jobs are inserted in the same transaction as the data change
// that caused them (river.Client.InsertTx), so work is never lost or duplicated by a crash.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
	"github.com/riverqueue/river/rivertype"

	"github.com/opshub/opshub/internal/mail"
	"github.com/opshub/opshub/internal/store"
)

// Inserter is the subset of *river.Client services depend on (mockable in tests).
type Inserter interface {
	InsertTx(ctx context.Context, tx pgx.Tx, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error)
}

// SendEmailArgs renders template in Locale for To. Data must not contain secrets other
// than single-use links, which is why email jobs are deleted soon after completion.
type SendEmailArgs struct {
	To       string         `json:"to"`
	Template string         `json:"template"`
	Locale   string         `json:"locale"`
	Data     map[string]any `json:"data"`
}

func (SendEmailArgs) Kind() string { return "send_email" }

func (SendEmailArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: QueueEmail, MaxAttempts: 8}
}

// SendEmailWorker renders and delivers emails.
type SendEmailWorker struct {
	river.WorkerDefaults[SendEmailArgs]
	Renderer *mail.Renderer
	Sender   mail.Sender
	// Register adds workers owned by feature packages (e.g. pipelines); Periodic adds their
	// periodic jobs.
	Register func(*river.Workers)
	Periodic []*river.PeriodicJob
}

func (w *SendEmailWorker) Work(ctx context.Context, job *river.Job[SendEmailArgs]) error {
	msg, err := w.Renderer.Render(job.Args.Template, job.Args.Locale, job.Args.Data)
	if err != nil {
		return river.JobCancel(err) // a template bug won't fix itself by retrying
	}
	msg.To = job.Args.To
	return w.Sender.Send(ctx, msg)
}

func (w *SendEmailWorker) Timeout(*river.Job[SendEmailArgs]) time.Duration { return time.Minute }

// CleanupAuthArgs purges expired sessions, email tokens and MFA challenges.
type CleanupAuthArgs struct{}

func (CleanupAuthArgs) Kind() string { return "cleanup_auth" }

type CleanupAuthWorker struct {
	river.WorkerDefaults[CleanupAuthArgs]
	Pool *pgxpool.Pool
}

func (w *CleanupAuthWorker) Work(ctx context.Context, _ *river.Job[CleanupAuthArgs]) error {
	return store.New(w.Pool).DeleteExpiredAuthRecords(ctx)
}

// HousekeepingArgs purges expired idempotency keys and webhook deliveries older than 30 days.
type HousekeepingArgs struct{}

func (HousekeepingArgs) Kind() string { return "housekeeping" }

type HousekeepingWorker struct {
	river.WorkerDefaults[HousekeepingArgs]
	Pool *pgxpool.Pool
}

func (w *HousekeepingWorker) Work(ctx context.Context, _ *river.Job[HousekeepingArgs]) error {
	return store.New(w.Pool).DeleteExpiredHousekeeping(ctx)
}

// PipelineFromEventArgs starts pipeline runs for a verified Git webhook event. It carries the
// parsed event so the worker doesn't need the raw payload.
type PipelineFromEventArgs struct {
	RepositoryID uuid.UUID `json:"repository_id"`
	ProjectID    uuid.UUID `json:"project_id"`
	Event        string    `json:"event"` // push, tag_push, pull_request
	Ref          string    `json:"ref"`
	SHA          string    `json:"sha"`
	BaseRef      string    `json:"base_ref,omitempty"`
	Title        string    `json:"title,omitempty"`
	Actor        string    `json:"actor,omitempty"`
}

func (PipelineFromEventArgs) Kind() string { return "pipeline_from_event" }

func (PipelineFromEventArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: QueuePipelines, MaxAttempts: 8}
}

// PipelineScheduleArgs starts one scheduled (cron) run. Unique per schedule and due time.
type PipelineScheduleArgs struct {
	ScheduleID uuid.UUID `json:"schedule_id"`
	Due        time.Time `json:"due"`
}

func (PipelineScheduleArgs) Kind() string { return "pipeline_schedule" }

func (PipelineScheduleArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: QueuePipelines, MaxAttempts: 5, UniqueOpts: river.UniqueOpts{ByArgs: true}}
}

// PipelineTickArgs runs every minute: due cron schedules, job timeouts, stuck queues.
type PipelineTickArgs struct{}

func (PipelineTickArgs) Kind() string { return "pipeline_tick" }

// RunnerHousekeepingArgs runs every 30 seconds: lost runners, expired artifacts, cache quota.
type RunnerHousekeepingArgs struct{}

func (RunnerHousekeepingArgs) Kind() string { return "runner_housekeeping" }

// DeploymentArgs performs one deployment. Deployments aren't idempotent, so a second
// attempt (after a crash) only marks the deployment interrupted.
type DeploymentArgs struct {
	DeploymentID uuid.UUID `json:"deployment_id"`
}

func (DeploymentArgs) Kind() string { return "deployment" }

func (DeploymentArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: QueueDeploys, MaxAttempts: 2, UniqueOpts: river.UniqueOpts{ByArgs: true}}
}

// InfraMaintenanceArgs runs hourly: metric rollups, retention and partitions.
type InfraMaintenanceArgs struct{}

func (InfraMaintenanceArgs) Kind() string { return "infra_maintenance" }

// CertificateChecksArgs probes the TLS certificates of domain assets that are due.
type CertificateChecksArgs struct{}

func (CertificateChecksArgs) Kind() string { return "certificate_checks" }

const (
	QueueDeploys   = "deploys"
	QueuePipelines = "pipelines"
	QueueDefault   = river.QueueDefault
	QueueEmail     = "email"
)

// Deps are the collaborators workers need.
type Deps struct {
	Pool     *pgxpool.Pool
	Logger   *slog.Logger
	Renderer *mail.Renderer
	Sender   mail.Sender
	// Register adds workers owned by feature packages (e.g. pipelines); Periodic adds their
	// periodic jobs.
	Register func(*river.Workers)
	Periodic []*river.PeriodicJob
}

// NewClient builds a River client that inserts and (after Start) works jobs.
func NewClient(d Deps) (*river.Client[pgx.Tx], error) {
	workers := river.NewWorkers()
	river.AddWorker(workers, &SendEmailWorker{Renderer: d.Renderer, Sender: d.Sender})
	river.AddWorker(workers, &CleanupAuthWorker{Pool: d.Pool})
	river.AddWorker(workers, &HousekeepingWorker{Pool: d.Pool})
	if d.Register != nil {
		d.Register(workers)
	}

	client, err := river.NewClient(riverpgxv5.New(d.Pool), &river.Config{
		Logger: d.Logger,
		Queues: map[string]river.QueueConfig{
			QueueDefault:   {MaxWorkers: 20},
			QueueEmail:     {MaxWorkers: 5},
			QueuePipelines: {MaxWorkers: 10},
			QueueDeploys:   {MaxWorkers: 10},
		},
		Workers: workers,
		PeriodicJobs: append([]*river.PeriodicJob{
			river.NewPeriodicJob(river.PeriodicInterval(time.Hour),
				func() (river.JobArgs, *river.InsertOpts) { return CleanupAuthArgs{}, nil },
				&river.PeriodicJobOpts{RunOnStart: true}),
			river.NewPeriodicJob(river.PeriodicInterval(time.Hour),
				func() (river.JobArgs, *river.InsertOpts) { return HousekeepingArgs{}, nil },
				&river.PeriodicJobOpts{RunOnStart: true}),
		}, d.Periodic...),
		// Completed jobs (including email payloads with one-time links) are removed after a day.
		CompletedJobRetentionPeriod: 24 * time.Hour,
	})
	if err != nil {
		return nil, fmt.Errorf("river client: %w", err)
	}
	return client, nil
}

// NewInsertOnlyClient returns a client that can enqueue jobs but never works them
// (used by the seed command and tests).
func NewInsertOnlyClient(pool *pgxpool.Pool) (*river.Client[pgx.Tx], error) {
	return river.NewClient(riverpgxv5.New(pool), &river.Config{})
}

// Migrate applies (up) or removes (down) River's schema.
func Migrate(ctx context.Context, pool *pgxpool.Pool, up bool) error {
	m, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		return err
	}
	if up {
		_, err = m.Migrate(ctx, rivermigrate.DirectionUp, nil)
	} else {
		_, err = m.Migrate(ctx, rivermigrate.DirectionDown, &rivermigrate.MigrateOpts{TargetVersion: -1})
	}
	if err != nil {
		return fmt.Errorf("river migrate: %w", err)
	}
	return nil
}

// Deferred is an Inserter bound to a client after construction. It breaks the start-up
// cycle where services need an inserter and the client needs those services' workers.
type Deferred struct {
	client Inserter
}

// Bind sets the client; call it before serving requests.
func (d *Deferred) Bind(c Inserter) { d.client = c }

func (d *Deferred) InsertTx(ctx context.Context, tx pgx.Tx, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	if d.client == nil {
		return nil, errors.New("jobs: inserter used before Bind")
	}
	return d.client.InsertTx(ctx, tx, args, opts)
}
