package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

// kubeExecutor deploys to a Kubernetes Deployment.
//
//   - rolling: patch the container image and wait for Kubernetes' rolling update; on failure
//     patch the previous image back.
//   - blue_green: <deployment>-blue and <deployment>-green run side by side. The idle color
//     gets the new image (created from the active one, or from <deployment> the first time),
//     and once it is ready the Service selector switches to it. The previous color keeps
//     running for an instant rollback. A failed health check switches back.
type kubeExecutor struct {
	cfg    KubernetesConfig
	kube   *kubeClient
	health healthChecker
}

func (x *kubeExecutor) timeout() time.Duration {
	return time.Duration(x.cfg.RolloutTimeoutSeconds) * time.Second
}

func (x *kubeExecutor) Deploy(ctx context.Context, r Release, log io.Writer) (Outcome, error) {
	if r.Strategy == "blue_green" {
		return x.blueGreen(ctx, r, log)
	}
	return x.rolling(ctx, r, log)
}

func unreachable(err error) error {
	if errors.Is(err, errKubeNotFound) {
		return fail(ReasonDeployFailed, "%v", err)
	}
	return fail(ReasonUnreachable, "kubernetes: %v", err)
}

func (x *kubeExecutor) rolling(ctx context.Context, r Release, log io.Writer) (Outcome, error) {
	var out Outcome
	ns, name := x.cfg.Namespace, x.cfg.Deployment
	d, err := x.kube.getDeployment(ctx, ns, name)
	if err != nil {
		return out, unreachable(fmt.Errorf("deployment %s/%s: %w", ns, name, err))
	}
	container, prevImage, err := d.container(x.cfg.Container)
	if err != nil {
		return out, fail(ReasonDeployFailed, "%v", err)
	}
	out.Previous = prevImage
	_, _ = fmt.Fprintf(log, "Updating %s/%s container %s: %s → %s\n", ns, name, container, prevImage, r.Version)
	if err := x.kube.setImage(ctx, ns, name, container, r.Version, r.DeploymentID.String(), nil); err != nil {
		return out, unreachable(err)
	}
	failure := func(err error) (Outcome, error) {
		_, _ = fmt.Fprintf(log, "Rolling back %s to %s\n", name, prevImage)
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), x.timeout())
		defer cancel()
		if rerr := x.kube.setImage(rctx, ns, name, container, prevImage, r.DeploymentID.String()+"-revert", nil); rerr != nil {
			_, _ = fmt.Fprintf(log, "Rollback failed: %v\n", rerr)
			return out, err
		}
		if rerr := x.kube.waitRollout(rctx, ns, name, x.timeout(), log); rerr != nil {
			_, _ = fmt.Fprintf(log, "Rollback didn't finish: %v\n", rerr)
			return out, err
		}
		out.Reverted = true
		return out, err
	}
	start := time.Now()
	if err := x.kube.waitRollout(ctx, ns, name, x.timeout(), log); err != nil {
		out.Health = append(out.Health, HealthResult{Target: "rollout " + name, Detail: err.Error(), At: time.Now()})
		return failure(fail(ReasonUnhealthy, "%v", err))
	}
	out.Health = append(out.Health, HealthResult{Target: "rollout " + name, OK: true, Attempts: 1,
		Detail: "ready in " + time.Since(start).Round(time.Second).String(), At: time.Now()})
	if x.cfg.HealthCheck != nil {
		res := x.health.check(ctx, *x.cfg.HealthCheck, "", log)
		out.Health = append(out.Health, res)
		if !res.OK {
			return failure(fail(ReasonUnhealthy, "health check failed: %s", res.Detail))
		}
	}
	return out, nil
}

func (x *kubeExecutor) blueGreen(ctx context.Context, r Release, log io.Writer) (Outcome, error) {
	var out Outcome
	ns, base := x.cfg.Namespace, x.cfg.Deployment
	if x.cfg.Service == "" {
		return out, fail(ReasonStrategyUnsupport, "blue/green needs the target's service")
	}
	svc, err := x.kube.getService(ctx, ns, x.cfg.Service)
	if err != nil {
		return out, unreachable(fmt.Errorf("service %s/%s: %w", ns, x.cfg.Service, err))
	}
	active := svc.Spec.Selector[colorLabel]
	idle := "blue"
	if active == "blue" {
		idle = "green"
	}
	source := base
	if active != "" {
		source = base + "-" + active
	}
	src, err := x.kube.getDeployment(ctx, ns, source)
	if err != nil {
		return out, unreachable(fmt.Errorf("deployment %s/%s: %w", ns, source, err))
	}
	container, prevImage, err := src.container(x.cfg.Container)
	if err != nil {
		return out, fail(ReasonDeployFailed, "%v", err)
	}
	out.Previous = prevImage
	replicas := src.replicas()
	target := base + "-" + idle
	if active == "" {
		_, _ = fmt.Fprintf(log, "No color is live yet; %s serves %s\n", x.cfg.Service, base)
	} else {
		_, _ = fmt.Fprintf(log, "Live color: %s (%s)\n", active, prevImage)
	}
	_, _ = fmt.Fprintf(log, "Deploying %s to %s (%d replicas)\n", r.Version, target, replicas)
	switch _, err := x.kube.getDeployment(ctx, ns, target); {
	case errors.Is(err, errKubeNotFound):
		if err := x.kube.cloneForColor(ctx, ns, source, target, idle, container, r.Version, r.DeploymentID.String(), replicas); err != nil {
			return out, unreachable(fmt.Errorf("create %s: %w", target, err))
		}
	case err != nil:
		return out, unreachable(err)
	default:
		if err := x.kube.setImage(ctx, ns, target, container, r.Version, r.DeploymentID.String(), &replicas); err != nil {
			return out, unreachable(err)
		}
	}
	start := time.Now()
	if err := x.kube.waitRollout(ctx, ns, target, x.timeout(), log); err != nil {
		out.Health = append(out.Health, HealthResult{Target: "rollout " + target, Detail: err.Error(), At: time.Now()})
		// Traffic never moved: the live color is untouched.
		out.Reverted = true
		return out, fail(ReasonUnhealthy, "%v", err)
	}
	out.Health = append(out.Health, HealthResult{Target: "rollout " + target, OK: true, Attempts: 1,
		Detail: "ready in " + time.Since(start).Round(time.Second).String(), At: time.Now()})
	_, _ = fmt.Fprintf(log, "Switching %s to %s\n", x.cfg.Service, idle)
	if err := x.kube.selectColor(ctx, ns, x.cfg.Service, idle); err != nil {
		return out, unreachable(err)
	}
	if x.cfg.HealthCheck != nil {
		res := x.health.check(ctx, *x.cfg.HealthCheck, "", log)
		out.Health = append(out.Health, res)
		if !res.OK {
			_, _ = fmt.Fprintf(log, "Switching %s back\n", x.cfg.Service)
			rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
			defer cancel()
			if err := x.kube.selectColor(rctx, ns, x.cfg.Service, active); err == nil {
				out.Reverted = true
			} else {
				_, _ = fmt.Fprintf(log, "Switch back failed: %v\n", err)
			}
			return out, fail(ReasonUnhealthy, "health check failed: %s", res.Detail)
		}
	}
	if active != "" {
		_, _ = fmt.Fprintf(log, "%s-%s keeps running %s for an instant rollback\n", base, active, prevImage)
	} else {
		_, _ = fmt.Fprintf(log, "%s no longer receives traffic from %s; scale it down when you're ready\n", base, x.cfg.Service)
	}
	return out, nil
}

func (x *kubeExecutor) Test(ctx context.Context) TestResult {
	res := TestResult{OK: true}
	v, err := x.kube.serverVersion(ctx)
	if err != nil {
		return TestResult{Checks: []Check{{Name: "cluster", Detail: err.Error()}}}
	}
	res.Checks = append(res.Checks, Check{Name: "cluster", OK: true, Detail: "Kubernetes " + v})
	name := x.cfg.Namespace + "/" + x.cfg.Deployment
	if d, err := x.kube.getDeployment(ctx, x.cfg.Namespace, x.cfg.Deployment); err != nil {
		res.OK = false
		res.Checks = append(res.Checks, Check{Name: "deployment " + name, Detail: err.Error()})
	} else if _, img, err := d.container(x.cfg.Container); err != nil {
		res.OK = false
		res.Checks = append(res.Checks, Check{Name: "deployment " + name, Detail: err.Error()})
	} else {
		res.Checks = append(res.Checks, Check{Name: "deployment " + name, OK: true, Detail: img})
	}
	if x.cfg.Service != "" {
		svc, err := x.kube.getService(ctx, x.cfg.Namespace, x.cfg.Service)
		if err != nil {
			res.OK = false
			res.Checks = append(res.Checks, Check{Name: "service " + x.cfg.Service, Detail: err.Error()})
		} else {
			detail := "no color live yet"
			if c := svc.Spec.Selector[colorLabel]; c != "" {
				detail = "live color: " + c
			}
			res.Checks = append(res.Checks, Check{Name: "service " + x.cfg.Service, OK: true, Detail: detail})
		}
	}
	return res
}

func (x *kubeExecutor) Close() error { return nil }
