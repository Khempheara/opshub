package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/opshub/opshub/internal/audit"
	"github.com/opshub/opshub/internal/database"
	"github.com/opshub/opshub/internal/events"
	"github.com/opshub/opshub/internal/pipeline"
	"github.com/opshub/opshub/internal/store"
)

// Limits for one deployment.
const (
	deployTimeout    = time.Hour
	maxLogBytes      = 5 << 20
	logFlushInterval = 500 * time.Millisecond
	logChunkBytes    = 32 << 10
)

// deployLog is an io.Writer storing a deployment's output in chunks (and mirroring it to its
// pipeline job's log). Writes never block on the database.
type deployLog struct {
	s       *Service
	id      uuid.UUID
	jobID   *uuid.UUID
	mu      sync.Mutex
	buf     bytes.Buffer
	seq     int32
	jobSeq  int32
	bytes   int64
	kick    chan struct{}
	done    chan struct{}
	stopped chan struct{}
}

func (s *Service) newLog(ctx context.Context, d store.Deployment) *deployLog {
	l := &deployLog{s: s, id: d.ID, jobID: d.JobID, seq: -1, kick: make(chan struct{}, 1), done: make(chan struct{}), stopped: make(chan struct{})}
	if d.JobID != nil {
		if n, err := store.New(s.pool).NextJobLogSeq(ctx, *d.JobID); err == nil {
			l.jobSeq = n
		}
	}
	go l.loop()
	return l
}

func (l *deployLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	l.buf.Write(bytes.ReplaceAll(p, []byte{0}, nil))
	full := l.buf.Len() >= logChunkBytes
	l.mu.Unlock()
	if full {
		select {
		case l.kick <- struct{}{}:
		default:
		}
	}
	return len(p), nil
}

func (l *deployLog) loop() {
	defer close(l.stopped)
	t := time.NewTicker(logFlushInterval)
	defer t.Stop()
	for {
		select {
		case <-l.done:
			for l.flush(true) {
			}
			return
		case <-t.C:
		case <-l.kick:
		}
		for l.flush(false) {
		}
	}
}

// flush stores one chunk (up to a line end unless final) and reports whether more is ready.
func (l *deployLog) flush(final bool) bool {
	l.mu.Lock()
	b := l.buf.Bytes()
	n := min(len(b), logChunkBytes)
	if !final && n == len(b) {
		if i := bytes.LastIndexByte(b[:n], '\n'); i >= 0 {
			n = i + 1
		} else if n < logChunkBytes {
			n = 0
		}
	}
	for n > 0 && n < len(b) && !utf8.RuneStart(b[n]) {
		n--
	}
	if n == 0 {
		l.mu.Unlock()
		return false
	}
	chunk := string(b[:n])
	l.buf.Next(n)
	more := l.buf.Len() >= logChunkBytes || (final && l.buf.Len() > 0)
	l.mu.Unlock()

	if l.bytes >= maxLogBytes {
		return more
	}
	if l.bytes+int64(len(chunk)) > maxLogBytes {
		chunk = chunk[:maxLogBytes-l.bytes] + "\n[OpsHub: log truncated at 5 MiB]\n"
	}
	l.seq++
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := database.InTx(ctx, l.s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if _, err := q.InsertDeploymentLog(ctx, store.InsertDeploymentLogParams{DeploymentID: l.id, Seq: l.seq, Content: chunk}); err != nil {
			return err
		}
		if err := q.AddDeploymentLogBytes(ctx, store.AddDeploymentLogBytesParams{ID: l.id, Bytes: int64(len(chunk))}); err != nil {
			return err
		}
		return events.NotifyDeployment(ctx, tx, l.id)
	})
	if err != nil {
		l.s.logger.Warn("deployment log write failed", "deployment_id", l.id, "error", err)
	}
	l.bytes += int64(len(chunk))
	if l.jobID != nil {
		// The job may have been canceled meanwhile; its log is a convenience copy.
		_ = l.s.pipelines.AppendLog(ctx, *l.jobID, l.jobSeq, chunk, nil)
		l.jobSeq++
	}
	return more
}

func (l *deployLog) Close() {
	select {
	case <-l.done:
	default:
		close(l.done)
	}
	<-l.stopped
}

// Run performs a deployment (the River worker). attempt > 1 means an earlier attempt was
// interrupted: the target's state is unknown, so the deployment fails without retrying.
func (s *Service) Run(ctx context.Context, id uuid.UUID, attempt int) error {
	q := store.New(s.pool)
	d, err := q.GetDeployment(ctx, id)
	if database.IsNoRows(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if d.Status == store.DeploymentStatusSucceeded || d.Status == store.DeploymentStatusFailed {
		return nil
	}
	if attempt > 1 || d.Status == store.DeploymentStatusRunning {
		l := s.newLog(ctx, d)
		_, _ = fmt.Fprintln(l, "OpsHub restarted while this deployment was running; check the target before deploying again.")
		l.Close()
		return s.finish(ctx, d, Outcome{}, fail(ReasonInterrupted, "interrupted"))
	}
	if d, err = q.StartDeployment(ctx, id); database.IsNoRows(err) {
		return nil
	} else if err != nil {
		return err
	}
	if err := events.NotifyDeployment(ctx, s.pool, id); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, deployTimeout)
	defer cancel()
	l := s.newLog(ctx, d)
	out, derr := s.execute(ctx, d, l)
	if derr != nil {
		_, _ = fmt.Fprintf(l, "\x1b[31;1mDeployment failed: %v\x1b[0m\n", derr)
		if out.Reverted {
			_, _ = fmt.Fprintln(l, "The previous release was restored.")
		}
	} else {
		_, _ = fmt.Fprintf(l, "\x1b[32;1mDeployed %s to %s.\x1b[0m\n", d.Version, d.TargetName)
	}
	l.Close()
	return s.finish(context.WithoutCancel(ctx), d, out, derr)
}

func (s *Service) execute(ctx context.Context, d store.Deployment, l *deployLog) (Outcome, error) {
	if d.TargetID == nil {
		return Outcome{}, fail(ReasonTargetMissing, "the target %s was deleted", d.TargetName)
	}
	q := store.New(s.pool)
	t, err := q.GetDeployTarget(ctx, *d.TargetID)
	if database.IsNoRows(err) {
		return Outcome{}, fail(ReasonTargetMissing, "the target %s was deleted", d.TargetName)
	}
	if err != nil {
		return Outcome{}, err
	}
	env, err := q.GetEnvironment(ctx, d.EnvironmentID)
	if err != nil {
		return Outcome{}, err
	}
	p, err := q.GetProjectForPipeline(ctx, d.ProjectID)
	if err != nil {
		return Outcome{}, err
	}
	if d.RollbackOfID != nil {
		_, _ = fmt.Fprintf(l, "Rolling back %s to %s\n", env.Environment.Name, d.Version)
	}
	_, _ = fmt.Fprintf(l, "Deploying %s to %s on %s (%s, %s)\n", d.Version, env.Environment.Name, t.Name, t.Kind, d.Strategy)
	if d.PreviousVersion != "" {
		_, _ = fmt.Fprintf(l, "Current release: %s\n", d.PreviousVersion)
	}
	x, err := s.open(ctx, t)
	if err != nil {
		var de *deployError
		if errors.As(err, &de) {
			return Outcome{}, err
		}
		return Outcome{}, fail(ReasonUnreachable, "%v", err)
	}
	defer func() { _ = x.Close() }()
	return x.Deploy(ctx, Release{
		DeploymentID: d.ID, Version: d.Version, Previous: d.PreviousVersion, Strategy: string(d.Strategy),
		Environment: env.Environment.Name, Project: p.Slug,
	}, l)
}

// finish records the result, updates the environment's current release and completes the
// pipeline job.
func (s *Service) finish(ctx context.Context, d store.Deployment, out Outcome, derr error) error {
	status := store.DeploymentStatusSucceeded
	var reason *string
	if derr != nil {
		status = store.DeploymentStatusFailed
		r := reasonOf(derr)
		reason = &r
	}
	health := out.Health
	if health == nil {
		health = []HealthResult{}
	}
	hj, _ := json.Marshal(health)
	err := database.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		f, err := q.FinishDeployment(ctx, store.FinishDeploymentParams{
			ID: d.ID, Status: status, FailureReason: reason, Reverted: out.Reverted, Health: hj, ObservedPrevious: out.Previous,
		})
		if database.IsNoRows(err) {
			return nil // finished meanwhile
		}
		if err != nil {
			return err
		}
		if status == store.DeploymentStatusSucceeded {
			if err := q.SetCurrentDeployment(ctx, store.SetCurrentDeploymentParams{ID: d.EnvironmentID, DeploymentID: &d.ID}); err != nil {
				return err
			}
		}
		if err := events.NotifyDeployment(ctx, tx, d.ID); err != nil {
			return err
		}
		return audit.Record(ctx, q, audit.Entry{
			OrganizationID: &d.OrganizationID, ProjectID: &d.ProjectID, Action: "deployment." + string(status),
			ResourceType: "deployment", ResourceID: d.ID.String(), ActorType: "system",
			After: map[string]any{
				"number": f.Number, "version": f.Version, "previous_version": f.PreviousVersion, "reason": reason,
				"reverted": f.Reverted, "rollback_of": f.RollbackOfID,
			},
		})
	})
	if err != nil {
		return err
	}
	if d.JobID != nil {
		jobReason := ""
		if derr != nil {
			jobReason = pipeline.ReasonDeployFailed
		}
		if err := s.pipelines.Complete(ctx, *d.JobID, derr == nil, nil, jobReason); err != nil {
			s.logger.Info("deploy job not completed", "job_id", *d.JobID, "error", err)
		}
	}
	s.logger.Info("deployment finished", "deployment_id", d.ID, "status", status, "reverted", out.Reverted)
	return nil
}
