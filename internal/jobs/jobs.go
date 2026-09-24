// Package jobs wires the River background job queue: job argument types, workers, periodic
// jobs and schema migrations. Jobs are inserted in the same transaction as the data change
// that caused them (river.Client.InsertTx), so work is never lost or duplicated by a crash.
package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"time"

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

const (
	QueueDefault = river.QueueDefault
	QueueEmail   = "email"
)

// Deps are the collaborators workers need.
type Deps struct {
	Pool     *pgxpool.Pool
	Logger   *slog.Logger
	Renderer *mail.Renderer
	Sender   mail.Sender
}

// NewClient builds a River client that inserts and (after Start) works jobs.
func NewClient(d Deps) (*river.Client[pgx.Tx], error) {
	workers := river.NewWorkers()
	river.AddWorker(workers, &SendEmailWorker{Renderer: d.Renderer, Sender: d.Sender})
	river.AddWorker(workers, &CleanupAuthWorker{Pool: d.Pool})
	river.AddWorker(workers, &HousekeepingWorker{Pool: d.Pool})

	client, err := river.NewClient(riverpgxv5.New(d.Pool), &river.Config{
		Logger: d.Logger,
		Queues: map[string]river.QueueConfig{
			QueueDefault: {MaxWorkers: 20},
			QueueEmail:   {MaxWorkers: 5},
		},
		Workers: workers,
		PeriodicJobs: []*river.PeriodicJob{
			river.NewPeriodicJob(river.PeriodicInterval(time.Hour),
				func() (river.JobArgs, *river.InsertOpts) { return CleanupAuthArgs{}, nil },
				&river.PeriodicJobOpts{RunOnStart: true}),
			river.NewPeriodicJob(river.PeriodicInterval(time.Hour),
				func() (river.JobArgs, *river.InsertOpts) { return HousekeepingArgs{}, nil },
				&river.PeriodicJobOpts{RunOnStart: true}),
		},
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
