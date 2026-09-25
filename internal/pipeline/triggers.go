package pipeline

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/database"
	"github.com/opshub/opshub/internal/gitprovider"
	"github.com/opshub/opshub/internal/jobs"
	"github.com/opshub/opshub/internal/pipeline/spec"
	"github.com/opshub/opshub/internal/store"
)

// pipelineProject loads a live project with a connected repository, or reports ok=false.
func (s *Service) pipelineProject(ctx context.Context, q *store.Queries, id uuid.UUID) (store.Project, *uuid.UUID, bool, error) {
	p, err := q.GetProjectForPipeline(ctx, id)
	if database.IsNoRows(err) {
		return store.Project{}, nil, false, nil
	}
	if err != nil {
		return store.Project{}, nil, false, err
	}
	proj := store.Project{ID: p.ID, OrganizationID: p.OrganizationID, Slug: p.Slug, Name: p.Name, DefaultBranch: p.DefaultBranch}
	if p.RepositoryID == nil {
		return proj, nil, false, nil
	}
	return proj, p.RepositoryID, true, nil
}

// fileMissing reports a PIPELINE_FILE_NOT_FOUND error: the repository has no pipeline.
func fileMissing(err error) bool {
	ae, ok := apperr.From(err)
	return ok && ae.Code == apperr.CodePipelineFileNotFound
}

// RunFromEvent creates a run for a verified push, tag or pull request event when the
// pipeline's triggers match. An invalid `.opshub.yml` produces a failed run showing the
// problems; a repository without the file is ignored. Errors reaching the Git host are
// returned so the job is retried.
func (s *Service) RunFromEvent(ctx context.Context, a jobs.PipelineFromEventArgs) error {
	q := store.New(s.pool)
	p, repoID, ok, err := s.pipelineProject(ctx, q, a.ProjectID)
	if err != nil || !ok || *repoID != a.RepositoryID {
		return err // project deleted or repository replaced: nothing to do
	}
	var trigger store.RunTrigger
	switch a.Event {
	case "push":
		trigger = store.RunTriggerPush
	case "tag_push":
		trigger = store.RunTriggerTag
	case "pull_request":
		trigger = store.RunTriggerPullRequest
	default:
		return nil
	}
	exists, err := q.RecentRunExists(ctx, store.RecentRunExistsParams{ProjectID: p.ID, Trigger: trigger, Ref: a.Ref, CommitSha: a.SHA})
	if err != nil || exists {
		return err
	}
	client, _, err := s.projects.GitClient(ctx, q, p.ID)
	if err != nil {
		return err
	}
	def, problems, err := s.fetchDefinition(ctx, client, a.SHA)
	if fileMissing(err) {
		return nil
	}
	if err != nil {
		return err
	}
	onDefault := trigger == store.RunTriggerPush && a.Ref == "refs/heads/"+p.DefaultBranch
	matched := len(problems) > 0 // invalid files always surface as a failed run
	if def != nil {
		switch trigger {
		case store.RunTriggerPush:
			matched = def.Triggers.Push.Matches(refName(a.Ref))
		case store.RunTriggerTag:
			matched = def.Triggers.Tag.Matches(refName(a.Ref))
		case store.RunTriggerPullRequest:
			matched = def.Triggers.PullRequest.Matches(a.BaseRef)
		}
	}
	return s.inTx(ctx, func(tx pgx.Tx, q *store.Queries) error {
		if onDefault && def != nil {
			if err := s.syncSchedules(ctx, q, p.ID, def); err != nil {
				return err
			}
		}
		if !matched {
			return nil
		}
		_, err := s.createRun(ctx, tx, q, newRun{
			project: p, trigger: trigger, ref: a.Ref, sha: a.SHA, title: a.Title, actor: a.Actor,
			def: def, problems: problems,
		})
		return err
	})
}

// RunFromSchedule creates a scheduled run on the project's default branch.
func (s *Service) RunFromSchedule(ctx context.Context, a jobs.PipelineScheduleArgs) error {
	q := store.New(s.pool)
	sc, err := q.GetSchedule(ctx, a.ScheduleID)
	if database.IsNoRows(err) {
		return nil // removed from the pipeline since
	}
	if err != nil {
		return err
	}
	p, _, ok, err := s.pipelineProject(ctx, q, sc.ProjectID)
	if err != nil || !ok {
		return err
	}
	client, _, err := s.projects.GitClient(ctx, q, p.ID)
	if err != nil {
		return err
	}
	commit, err := client.Commit(ctx, p.DefaultBranch)
	if errors.Is(err, gitprovider.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	def, problems, err := s.fetchDefinition(ctx, client, commit.SHA)
	if fileMissing(err) {
		// No pipeline any more: drop its schedules.
		return q.DeleteSchedulesExcept(ctx, store.DeleteSchedulesExceptParams{ProjectID: p.ID, Keep: []string{}})
	}
	if err != nil {
		return err
	}
	return s.inTx(ctx, func(tx pgx.Tx, q *store.Queries) error {
		if def != nil {
			if err := s.syncSchedules(ctx, q, p.ID, def); err != nil {
				return err
			}
			if !scheduled(def, sc.Cron) {
				return nil // the schedule was just removed from the file
			}
		}
		_, err := s.createRun(ctx, tx, q, newRun{
			project: p, trigger: store.RunTriggerSchedule, ref: "refs/heads/" + p.DefaultBranch, sha: commit.SHA,
			title: firstLine(commit.Message), def: def, problems: problems,
		})
		return err
	})
}

func scheduled(def *spec.Definition, cron string) bool {
	for _, sc := range def.Triggers.Schedules {
		if strings.TrimSpace(sc.Cron) == strings.TrimSpace(cron) {
			return true
		}
	}
	return false
}

// Enqueuer inserts a background job in a transaction (river.Client.InsertTx).
type Enqueuer func(ctx context.Context, tx pgx.Tx, args jobs.PipelineScheduleArgs) error

// Tick runs every minute: it enqueues due cron schedules and fails jobs that exceeded their
// timeout or waited 24 hours for a runner.
func (s *Service) Tick(ctx context.Context, enqueue Enqueuer) error {
	now := s.now()
	err := s.inTx(ctx, func(tx pgx.Tx, q *store.Queries) error {
		due, err := q.DueSchedules(ctx)
		if err != nil {
			return err
		}
		for _, sc := range due {
			if err := enqueue(ctx, tx, jobs.PipelineScheduleArgs{ScheduleID: sc.ID, Due: sc.NextRunAt.UTC()}); err != nil {
				return err
			}
			next := now.Add(24 * time.Hour)
			if c, err := spec.ParseCron(sc.Cron); err == nil {
				if n := c.Next(now); !n.IsZero() {
					next = n
				}
			}
			if err := q.AdvanceSchedule(ctx, store.AdvanceScheduleParams{ID: sc.ID, NextRunAt: next}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	q := store.New(s.pool)
	expired, err := q.ExpiredRunningJobs(ctx)
	if err != nil {
		return err
	}
	for _, j := range expired {
		if err := s.failJob(ctx, j.ID, ReasonTimeout); err != nil {
			return err
		}
	}
	stale, err := q.StaleQueuedJobs(ctx)
	if err != nil {
		return err
	}
	for _, j := range stale {
		if err := s.failJob(ctx, j.ID, ReasonNoRunner); err != nil {
			return err
		}
	}
	return nil
}

// failJob fails a running or queued job (timeouts, no runner) and advances its run.
func (s *Service) failJob(ctx context.Context, jobID uuid.UUID, reason string) error {
	return s.inTx(ctx, func(tx pgx.Tx, q *store.Queries) error {
		j, err := q.GetJob(ctx, jobID)
		if err != nil {
			return err
		}
		if _, err := q.LockRun(ctx, j.RunID); err != nil {
			return err
		}
		if j, err = q.GetJob(ctx, jobID); err != nil {
			return err
		}
		if j.Status != store.JobStatusRunning && j.Status != store.JobStatusQueued {
			return nil // finished meanwhile
		}
		if err := s.setStatus(ctx, q, j.ID, store.JobStatusFailed, reason, nil); err != nil {
			return err
		}
		if err := q.FinishOpenSteps(ctx, store.FinishOpenStepsParams{JobID: j.ID, Status: store.StepStatusCanceled}); err != nil {
			return err
		}
		return s.advance(ctx, tx, q, j.RunID)
	})
}
