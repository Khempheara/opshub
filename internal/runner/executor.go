package runner

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/opshub/opshub/internal/dockerapi"
)

// Workspace is where the job's files live inside every step container.
const Workspace = "/workspace"

// Labels on everything the runner creates, for cleanup after a crash.
const (
	labelRunner = "io.opshub.runner"
	labelJob    = "io.opshub.job"
)

// Failure reasons reported to OpsHub.
const (
	ReasonStepFailed  = "step_failed"
	ReasonTimeout     = "timeout"
	ReasonRunnerError = "runner_error"
)

// ExecConfig limits and defaults for job containers.
type ExecConfig struct {
	RunnerID     string
	DefaultImage string // when a job sets no image
	AlwaysPull   bool
	NanoCPUs     int64 // 0 = no limit
	MemoryBytes  int64 // 0 = no limit
	PidsLimit    int64
	Network      string // Docker network for step containers ("" = default bridge)
	TempDir      string // where archives are staged before upload
}

// Executor runs jobs in Docker containers.
type Executor struct {
	docker *dockerapi.Client
	client *Client
	cfg    ExecConfig
	logger *slog.Logger
}

func NewExecutor(docker *dockerapi.Client, client *Client, cfg ExecConfig, logger *slog.Logger) *Executor {
	return &Executor{docker: docker, client: client, cfg: cfg, logger: logger}
}

// Causes for canceling a job's context.
var (
	ErrCanceledByServer = errors.New("canceled in OpsHub")
	ErrShutdown         = errors.New("runner shutting down")
)

// stepFailed is a step exiting non-zero.
type stepFailed struct{ code int }

func (e stepFailed) Error() string { return fmt.Sprintf("exit code %d", e.code) }

// Run executes j and reports its result. ctx is canceled (with a cause) when OpsHub cancels
// the job or the runner shuts down.
func (x *Executor) Run(ctx context.Context, j *Job) {
	logger := x.logger.With("job_id", j.Job.ID, "job", j.Job.Name)
	ctx, cancelGone := context.WithCancelCause(ctx)
	defer cancelGone(nil)
	logs := NewLogShipper(func(ctx context.Context, seq int, content string) error {
		return x.client.AppendLog(ctx, j, seq, content)
	}, j.Masks, func() { cancelGone(ErrCanceledByServer) })

	timeout := time.Duration(j.Job.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = time.Hour
	}
	jobCtx, cancel := context.WithTimeoutCause(ctx, timeout, context.DeadlineExceeded)
	defer cancel()

	start := time.Now()
	err := x.execute(jobCtx, j, logs)

	cause := context.Cause(ctx)
	var sf stepFailed
	success, reason := false, ""
	var exit *int
	switch {
	case errors.Is(cause, ErrCanceledByServer):
		logs.Notice("Job canceled.")
		logs.Close()
		logger.Info("job canceled by server")
		return // OpsHub already knows
	case errors.Is(cause, ErrShutdown):
		logs.Notice("The runner is shutting down; the job was stopped.")
		reason = ReasonRunnerError
	case err == nil:
		success = true
		zero := 0
		exit = &zero
		logs.Notice(fmt.Sprintf("Job succeeded in %s.", time.Since(start).Round(time.Second)))
	case errors.Is(jobCtx.Err(), context.DeadlineExceeded):
		logs.Notice(fmt.Sprintf("Job timed out after %s.", timeout))
		reason = ReasonTimeout
	case errors.As(err, &sf):
		logs.Notice(fmt.Sprintf("Job failed: step exited with code %d.", sf.code))
		exit = &sf.code
		reason = ReasonStepFailed
	default:
		logs.Notice("Job failed: " + err.Error())
		reason = ReasonRunnerError
	}
	logs.Close()

	rctx, rcancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer rcancel()
	for attempt := range 5 {
		err := x.client.Complete(rctx, j, success, exit, reason)
		if err == nil || IsCode(err, "JOB_NOT_RUNNING") || IsCode(err, "JOB_TOKEN_INVALID") {
			break
		}
		logger.Warn("report result failed", "error", err, "attempt", attempt+1)
		time.Sleep(time.Duration(attempt+1) * time.Second)
	}
	logger.Info("job finished", "success", success, "reason", reason, "duration", time.Since(start).Round(time.Millisecond))
}

func (x *Executor) execute(ctx context.Context, j *Job, logs *LogShipper) (err error) {
	image := j.Spec.Image
	if image == "" {
		image = x.cfg.DefaultImage
	}
	labels := map[string]string{labelRunner: x.cfg.RunnerID, labelJob: j.Job.ID}
	volume := "opshub-job-" + j.Job.ID
	cleanup := context.WithoutCancel(ctx)

	logs.Notice(fmt.Sprintf("Running on opshub-runner with image %s", image))
	if err := x.docker.EnsureImage(ctx, image, x.cfg.AlwaysPull, logs); err != nil {
		return err
	}
	if err := x.docker.CreateVolume(ctx, volume, labels); err != nil {
		return err
	}
	defer func() {
		cctx, cancel := context.WithTimeout(cleanup, time.Minute)
		defer cancel()
		if err := x.docker.RemoveVolume(cctx, volume); err != nil {
			x.logger.Warn("remove volume failed", "volume", volume, "error", err)
		}
	}()

	// A never-started helper container gives access to the volume for copying files in and out.
	helper, err := x.docker.CreateContainer(ctx, dockerapi.ContainerSpec{
		Name: "opshub-" + j.Job.ID + "-files", Image: image, Entrypoint: []string{"/bin/sh", "-c"}, Cmd: []string{"true"},
		Labels: labels, Binds: []string{volume + ":" + Workspace}, Init: true,
	})
	if err != nil {
		return err
	}
	defer x.remove(cleanup, helper)

	if err := x.prepare(ctx, j, helper, logs); err != nil {
		return err
	}

	env := jobEnv(j.Variables, j.Secrets)

	for i, step := range j.Spec.Steps {
		if err := x.step(ctx, j, i, step, image, env, labels, volume, logs); err != nil {
			return err
		}
	}

	if j.Spec.Artifacts != nil && len(j.Spec.Artifacts.Paths) > 0 {
		if err := x.uploadArtifacts(ctx, j, helper, logs); err != nil {
			return fmt.Errorf("upload artifacts: %w", err)
		}
	}
	if j.Spec.Cache != nil && j.Spec.Cache.Key != "" && len(j.Spec.Cache.Paths) > 0 {
		if err := x.saveCache(ctx, j, helper, logs); err != nil {
			// A cache is an optimization: never fail a job for it.
			logs.Notice("Saving the cache failed: " + err.Error())
		}
	}
	return nil
}

func (x *Executor) remove(ctx context.Context, id string) {
	cctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if err := x.docker.RemoveContainer(cctx, id); err != nil {
		x.logger.Warn("remove container failed", "container", id, "error", err)
	}
}

// prepare fills the workspace: source, dependency artifacts, cache.
func (x *Executor) prepare(ctx context.Context, j *Job, helper string, logs *LogShipper) error {
	src, err := x.client.Source(ctx, j)
	if err != nil {
		return fmt.Errorf("download source: %w", err)
	}
	if src == nil {
		logs.Notice("No repository connected: starting with an empty workspace.")
	} else {
		stripped := StripSource(src)
		err := x.docker.PutArchive(ctx, helper, Workspace, stripped)
		_ = stripped.Close()
		_ = src.Close()
		if err != nil {
			return fmt.Errorf("unpack source: %w", err)
		}
		logs.Notice("Checked out " + short(j.CommitSHA) + ".")
	}

	if len(j.Spec.Needs) > 0 {
		deps, err := x.client.Dependencies(ctx, j)
		if err != nil {
			return fmt.Errorf("list dependencies: %w", err)
		}
		for _, d := range deps {
			body, err := x.client.Dependency(ctx, j, d.ID)
			if err != nil {
				return fmt.Errorf("download artifacts of %s: %w", d.JobName, err)
			}
			err = x.docker.PutArchive(ctx, helper, Workspace, body) // Docker unpacks gzip itself
			_ = body.Close()
			if err != nil {
				return fmt.Errorf("unpack artifacts of %s: %w", d.JobName, err)
			}
			logs.Notice("Restored artifacts from " + d.JobName + ".")
		}
	}

	if c := j.Spec.Cache; c != nil && c.Key != "" {
		body, err := x.client.Cache(ctx, j, c.Key)
		switch {
		case err != nil:
			logs.Notice("Restoring the cache failed: " + err.Error())
		case body == nil:
			logs.Notice("No cache for key " + c.Key + ".")
		default:
			err = x.docker.PutArchive(ctx, helper, Workspace, body)
			_ = body.Close()
			if err != nil {
				logs.Notice("Restoring the cache failed: " + err.Error())
			} else {
				logs.Notice("Restored cache " + c.Key + ".")
			}
		}
	}
	return nil
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func (x *Executor) step(ctx context.Context, j *Job, i int, s Step, image string, env []string, labels map[string]string, volume string, logs *LogShipper) error {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := x.client.StepStatus(rctx, j, i, "running", nil); err != nil {
		if IsCode(err, "JOB_NOT_RUNNING") {
			return context.Canceled
		}
		return fmt.Errorf("report step: %w", err)
	}
	_, _ = logs.Write([]byte("\x1b[36;1m$ " + firstLine(s.Run) + "\x1b[0m\n"))

	id, err := x.docker.CreateContainer(ctx, dockerapi.ContainerSpec{
		Name: fmt.Sprintf("opshub-%s-step%d", j.Job.ID, i), Image: image,
		Entrypoint: []string{"/bin/sh", "-ec"}, Cmd: []string{s.Run}, Env: env, WorkingDir: Workspace,
		Labels: labels, Binds: []string{volume + ":" + Workspace}, Init: true,
		NanoCPUs: x.cfg.NanoCPUs, Memory: x.cfg.MemoryBytes, PidsLimit: x.cfg.PidsLimit, Network: x.cfg.Network,
	})
	if err != nil {
		return err
	}
	cleanup := context.WithoutCancel(ctx)
	defer x.remove(cleanup, id)
	if err := x.docker.StartContainer(ctx, id); err != nil {
		return err
	}
	logsDone := make(chan error, 1)
	go func() { logsDone <- x.docker.FollowLogs(ctx, id, logs) }()

	code, err := x.docker.WaitContainer(ctx, id)
	if ctx.Err() != nil {
		kctx, kcancel := context.WithTimeout(cleanup, 30*time.Second)
		_ = x.docker.KillContainer(kctx, id)
		kcancel()
		<-logsDone
		x.finishStep(cleanup, j, i, "canceled", nil)
		return ctx.Err()
	}
	if err != nil {
		return err
	}
	// The log stream ends when the container exits; wait for its tail.
	select {
	case <-logsDone:
	case <-time.After(10 * time.Second):
	}
	if code != 0 {
		x.finishStep(cleanup, j, i, "failed", &code)
		return stepFailed{code: code}
	}
	x.finishStep(cleanup, j, i, "succeeded", &code)
	return nil
}

func (x *Executor) finishStep(ctx context.Context, j *Job, i int, status string, code *int) {
	rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := x.client.StepStatus(rctx, j, i, status, code); err != nil && !IsCode(err, "JOB_NOT_RUNNING") {
		x.logger.Warn("report step failed", "job_id", j.Job.ID, "step", i, "error", err)
	}
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + " …"
	}
	return s
}

// collect writes the given workspace paths into a gzip-compressed tar file and returns it
// (nil when none of the paths exist).
func (x *Executor) collect(ctx context.Context, helper string, paths []string, logs *LogShipper) (*os.File, error) {
	f, err := os.CreateTemp(x.cfg.TempDir, "opshub-archive-*.tar.gz")
	if err != nil {
		return nil, err
	}
	keep := false
	defer func() {
		if !keep {
			_ = f.Close()
			_ = os.Remove(f.Name())
		}
	}()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	total := 0
	for _, p := range paths {
		rel, err := safeRel(p)
		if err != nil {
			logs.Notice("Skipping " + p + ": " + err.Error())
			continue
		}
		rc, err := x.docker.GetArchive(ctx, helper, path.Join(Workspace, rel))
		if errors.Is(err, dockerapi.ErrNotFound) {
			logs.Notice("Nothing at " + p + ".")
			continue
		}
		if err != nil {
			return nil, err
		}
		n, err := Repack(tw, rc, path.Dir(rel))
		_ = rc.Close()
		if err != nil {
			return nil, err
		}
		total += n
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	if total == 0 {
		return nil, nil
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	keep = true
	return f, nil
}

func closeTemp(f *os.File) {
	_ = f.Close()
	_ = os.Remove(f.Name())
}

func (x *Executor) uploadArtifacts(ctx context.Context, j *Job, helper string, logs *LogShipper) error {
	f, err := x.collect(ctx, helper, j.Spec.Artifacts.Paths, logs)
	if err != nil || f == nil {
		if f == nil && err == nil {
			logs.Notice("No artifacts found.")
		}
		return err
	}
	defer closeTemp(f)
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if err := x.client.UploadArtifacts(ctx, j, f); err != nil {
		return err
	}
	logs.Notice(fmt.Sprintf("Uploaded artifacts (%s).", humanBytes(info.Size())))
	return nil
}

func (x *Executor) saveCache(ctx context.Context, j *Job, helper string, logs *LogShipper) error {
	f, err := x.collect(ctx, helper, j.Spec.Cache.Paths, logs)
	if err != nil || f == nil {
		return err
	}
	defer closeTemp(f)
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if err := x.client.PutCache(ctx, j, j.Spec.Cache.Key, f); err != nil {
		return err
	}
	logs.Notice(fmt.Sprintf("Saved cache %s (%s).", j.Spec.Cache.Key, humanBytes(info.Size())))
	return nil
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// Cleanup removes containers and volumes a previous run of this runner left behind.
func (x *Executor) Cleanup(ctx context.Context) {
	label := labelRunner + "=" + x.cfg.RunnerID
	if ids, err := x.docker.ListContainers(ctx, label); err == nil {
		for _, id := range ids {
			x.remove(ctx, id)
		}
	}
	if vols, err := x.docker.ListVolumes(ctx, label); err == nil {
		for _, v := range vols {
			_ = x.docker.RemoveVolume(ctx, v)
		}
	}
}

// jobEnv is the containers' environment: variables, then secrets (a secret wins over a
// variable of the same name), then OPSHUB_WORKSPACE; sorted for stable output.
func jobEnv(vars, secrets map[string]string) []string {
	merged := make(map[string]string, len(vars)+len(secrets)+1)
	maps.Copy(merged, vars)
	maps.Copy(merged, secrets)
	merged["OPSHUB_WORKSPACE"] = Workspace
	env := make([]string, 0, len(merged))
	for k, v := range merged {
		env = append(env, k+"="+v)
	}
	sort.Strings(env)
	return env
}
