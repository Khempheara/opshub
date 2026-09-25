package runner

import (
	"context"
	"errors"
	"log/slog"
	"runtime"
	"sync"
	"time"
)

// Agent polls OpsHub for jobs and runs up to MaxConcurrency of them at once.
type Agent struct {
	cfg      Config
	version  string
	client   *Client
	executor *Executor
	logger   *slog.Logger

	mu      sync.Mutex
	running map[string]context.CancelCauseFunc
}

func NewAgent(cfg Config, version string, client *Client, executor *Executor, logger *slog.Logger) *Agent {
	return &Agent{cfg: cfg, version: version, client: client, executor: executor, logger: logger, running: map[string]context.CancelCauseFunc{}}
}

// ErrUnauthorized means the runner token was revoked (the runner was deleted).
var ErrUnauthorized = errors.New("the runner token was rejected: the runner was probably deleted; register it again")

// Run works until ctx is canceled, then waits up to grace for running jobs before stopping
// them (they are reported as runner_error).
func (a *Agent) Run(ctx context.Context, grace time.Duration) error {
	a.executor.Cleanup(ctx)
	a.logger.Info("runner started", "name", a.cfg.Name, "labels", a.cfg.Labels, "max_concurrency", a.cfg.MaxConcurrency)

	jobCtx, stopJobs := context.WithCancelCause(context.WithoutCancel(ctx))
	defer stopJobs(nil)
	fatal := make(chan error, 1)
	var workers sync.WaitGroup

	// Heartbeats continue while jobs drain, so OpsHub doesn't consider them lost.
	hbCtx, stopHeartbeats := context.WithCancel(context.WithoutCancel(ctx))
	defer stopHeartbeats()
	go a.heartbeats(hbCtx, fatal)
	wctx, stopWorkers := context.WithCancel(ctx)
	defer stopWorkers()
	for range a.cfg.MaxConcurrency {
		workers.Add(1)
		go func() {
			defer workers.Done()
			a.worker(wctx, jobCtx, fatal)
		}()
	}

	var err error
	select {
	case <-ctx.Done():
	case err = <-fatal:
	}
	stopWorkers()
	a.logger.Info("stopping: no new jobs", "running", a.count())
	done := make(chan struct{})
	go func() { workers.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(grace):
		a.logger.Warn("stopping running jobs", "running", a.count())
		a.mu.Lock()
		for _, cancel := range a.running {
			cancel(ErrShutdown)
		}
		a.mu.Unlock()
		<-done
	}
	stopHeartbeats()
	return err
}

func (a *Agent) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.running)
}

// worker is one job slot: request a job, run it, repeat.
func (a *Agent) worker(ctx, jobCtx context.Context, fatal chan<- error) {
	backoff := time.Second
	for ctx.Err() == nil {
		rctx, cancel := context.WithTimeout(ctx, 45*time.Second)
		j, err := a.client.RequestJob(rctx, a.cfg.Token, 30*time.Second)
		cancel()
		switch {
		case ctx.Err() != nil:
			return
		case IsCode(err, "RUNNER_TOKEN_INVALID"):
			select {
			case fatal <- ErrUnauthorized:
			default:
			}
			return
		case IsCode(err, "RUNNER_DISABLED"):
			a.logger.Warn("runner is disabled in OpsHub; waiting")
			sleep(ctx, 30*time.Second)
			continue
		case err != nil:
			a.logger.Warn("request job failed", "error", err, "retry_in", backoff)
			sleep(ctx, backoff)
			backoff = min(backoff*2, time.Minute)
			continue
		case j == nil:
			backoff = time.Second
			continue
		}
		backoff = time.Second
		a.logger.Info("job started", "job_id", j.Job.ID, "job", j.Job.Name, "run", j.RunNumber)
		one, cancelJob := context.WithCancelCause(jobCtx)
		a.mu.Lock()
		a.running[j.Job.ID] = cancelJob
		a.mu.Unlock()
		a.executor.Run(one, j)
		a.mu.Lock()
		delete(a.running, j.Job.ID)
		a.mu.Unlock()
		cancelJob(nil)
	}
}

// heartbeats reports liveness and stops jobs OpsHub no longer wants.
func (a *Agent) heartbeats(ctx context.Context, fatal chan<- error) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		a.mu.Lock()
		ids := make([]string, 0, len(a.running))
		for id := range a.running {
			ids = append(ids, id)
		}
		a.mu.Unlock()
		hctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		stop, err := a.client.Heartbeat(hctx, a.cfg.Token, a.version, runtime.GOOS, runtime.GOARCH, ids)
		cancel()
		switch {
		case IsCode(err, "RUNNER_TOKEN_INVALID"):
			select {
			case fatal <- ErrUnauthorized:
			default:
			}
			return
		case err != nil && ctx.Err() == nil && !IsCode(err, "RUNNER_DISABLED"):
			a.logger.Warn("heartbeat failed", "error", err)
		}
		a.mu.Lock()
		for _, id := range stop {
			if c, ok := a.running[id]; ok {
				a.logger.Info("stopping job canceled in OpsHub", "job_id", id)
				c(ErrCanceledByServer)
			}
		}
		a.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}
