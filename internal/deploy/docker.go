package deploy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/opshub/opshub/internal/dockerapi"
)

// dockerExecutor replaces a target's containers one replica at a time. The replaced
// container is stopped and kept as <name>-previous until every replica is healthy, so a
// failure restores the previous release instantly.
type dockerExecutor struct {
	cfg    DockerConfig
	docker *dockerapi.Client
	ssh    *ssh.Client // Docker over SSH; nil otherwise
	health healthChecker
}

// connectDocker opens the connection a Docker target describes.
func connectDocker(ctx context.Context, cfg DockerConfig, creds DockerCredentials, netDialer *net.Dialer) (*dockerapi.Client, *ssh.Client, error) {
	var c *dockerapi.Client
	var sc *ssh.Client
	var err error
	switch cfg.Connection {
	case DockerViaLocal:
		c, err = dockerapi.NewUnix(ctx, cfg.Socket)
	case DockerViaSSH:
		d, derr := newSSHDialer(netDialer, cfg.User, SSHCredentials{PrivateKey: creds.PrivateKey, Passphrase: creds.Passphrase})
		if derr != nil {
			return nil, nil, derr
		}
		sc, err = d.dial(ctx, cfg.Host, cfg.HostKey)
		if errors.Is(err, errHostKey) {
			return nil, nil, fail(ReasonHostKeyUntrusted, "%s: host key isn't trusted; run Test connection and trust it", cfg.Host)
		}
		if err != nil {
			return nil, nil, fail(ReasonUnreachable, "%s: %v", cfg.Host, err)
		}
		c, err = dockerapi.New(ctx, func(ctx context.Context) (net.Conn, error) {
			return sc.DialContext(ctx, "unix", cfg.Socket)
		}, nil)
	case DockerViaTLS:
		tlsConf := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: hostOnly(cfg.Host)}
		if creds.CACert != "" {
			pool := x509.NewCertPool()
			pool.AppendCertsFromPEM([]byte(creds.CACert))
			tlsConf.RootCAs = pool
		}
		if creds.ClientCert != "" {
			pair, perr := tls.X509KeyPair([]byte(creds.ClientCert), []byte(creds.ClientKey))
			if perr != nil {
				return nil, nil, perr
			}
			tlsConf.Certificates = []tls.Certificate{pair}
		}
		c, err = dockerapi.New(ctx, func(ctx context.Context) (net.Conn, error) {
			return netDialer.DialContext(ctx, "tcp", cfg.Host)
		}, tlsConf)
	default:
		return nil, nil, fmt.Errorf("unknown connection %q", cfg.Connection)
	}
	if err != nil {
		if sc != nil {
			_ = sc.Close()
		}
		var de *deployError
		if errors.As(err, &de) {
			return nil, nil, err
		}
		return nil, nil, fail(ReasonUnreachable, "docker: %v", err)
	}
	if creds.RegistryUsername != "" {
		c = c.WithRegistry(dockerapi.Registry{Server: creds.RegistryServer, Username: creds.RegistryUsername, Password: creds.RegistryPassword})
	}
	return c, sc, nil
}

func (x *dockerExecutor) names() []string {
	if x.cfg.Replicas <= 1 {
		return []string{x.cfg.Container}
	}
	out := make([]string, x.cfg.Replicas)
	for i := range out {
		out[i] = fmt.Sprintf("%s-%d", x.cfg.Container, i+1)
	}
	return out
}

// healthHost is what {host} means in the health check URL.
func (x *dockerExecutor) healthHost() string {
	if x.cfg.Connection == DockerViaLocal {
		return "localhost"
	}
	return hostOnly(x.cfg.Host)
}

func (x *dockerExecutor) spec(r Release, name string) dockerapi.ContainerSpec {
	env := make([]string, 0, len(x.cfg.Env)+2)
	for k, v := range x.cfg.Env {
		env = append(env, k+"="+v)
	}
	sort.Strings(env)
	restart := x.cfg.Restart
	if restart == "no" {
		restart = ""
	}
	return dockerapi.ContainerSpec{
		Name: name, Image: r.Version, Env: env, Ports: x.cfg.Ports, Network: x.cfg.Network, Restart: restart,
		Labels: map[string]string{
			"io.opshub.deployment": r.DeploymentID.String(), "io.opshub.environment": r.Environment, "io.opshub.project": r.Project,
		},
	}
}

// waitReady waits for a started container to keep running (and pass its own HEALTHCHECK,
// when the image has one).
func (x *dockerExecutor) waitReady(ctx context.Context, name string, log io.Writer) error {
	deadline := time.Now().Add(60 * time.Second)
	if x.cfg.HealthCheck != nil && x.cfg.HealthCheck.TimeoutSeconds > 0 {
		deadline = time.Now().Add(time.Duration(x.cfg.HealthCheck.TimeoutSeconds) * time.Second)
	}
	stableSince := time.Time{}
	for {
		info, err := x.docker.InspectContainer(ctx, name)
		if err != nil {
			return err
		}
		if !info.Running {
			tail, _ := x.docker.Logs(ctx, info.ID, 20)
			if tail != "" {
				_, _ = fmt.Fprintf(log, "Last output of %s:\n%s", name, tail)
			}
			return fail(ReasonUnhealthy, "%s stopped (%s, exit code %d)", name, info.Status, info.Exit)
		}
		switch info.Health {
		case "healthy":
			return nil
		case "unhealthy":
			return fail(ReasonUnhealthy, "%s reports unhealthy", name)
		case "":
			// No HEALTHCHECK: running for 3 seconds counts as started.
			if stableSince.IsZero() {
				stableSince = time.Now()
			} else if time.Since(stableSince) >= 3*time.Second {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fail(ReasonUnhealthy, "%s didn't become healthy in time", name)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

type replaced struct {
	name        string
	hadPrevious bool
}

func (x *dockerExecutor) Deploy(ctx context.Context, r Release, log io.Writer) (Outcome, error) {
	var out Outcome
	_, _ = fmt.Fprintf(log, "Pulling %s\n", r.Version)
	if err := x.docker.EnsureImage(ctx, r.Version, true, log); err != nil {
		return out, fail(ReasonDeployFailed, "pull %s: %v", r.Version, err)
	}
	var done []replaced
	failure := func(err error) (Outcome, error) {
		out.Reverted = x.revert(context.WithoutCancel(ctx), done, log)
		return out, err
	}
	for _, name := range x.names() {
		prev := name + "-previous"
		_ = x.docker.RemoveContainer(ctx, prev) // a leftover from an interrupted deployment
		rep := replaced{name: name}
		info, err := x.docker.InspectContainer(ctx, name)
		switch {
		case err == nil:
			if out.Previous == "" {
				out.Previous = info.Image
			}
			_, _ = fmt.Fprintf(log, "Stopping %s (%s)\n", name, info.Image)
			if err := x.docker.StopContainer(ctx, info.ID, 20*time.Second); err != nil {
				return failure(fail(ReasonDeployFailed, "stop %s: %v", name, err))
			}
			if err := x.docker.RenameContainer(ctx, info.ID, prev); err != nil {
				return failure(fail(ReasonDeployFailed, "rename %s: %v", name, err))
			}
			rep.hadPrevious = true
		case errors.Is(err, dockerapi.ErrNotFound):
		default:
			return failure(fail(ReasonUnreachable, "inspect %s: %v", name, err))
		}
		done = append(done, rep)
		_, _ = fmt.Fprintf(log, "Starting %s with %s\n", name, r.Version)
		id, err := x.docker.CreateContainer(ctx, x.spec(r, name))
		if err != nil {
			return failure(fail(ReasonDeployFailed, "create %s: %v", name, err))
		}
		if err := x.docker.StartContainer(ctx, id); err != nil {
			return failure(fail(ReasonDeployFailed, "start %s: %v", name, err))
		}
		if err := x.waitReady(ctx, name, log); err != nil {
			out.Health = append(out.Health, HealthResult{Target: name, Detail: err.Error(), At: time.Now()})
			return failure(err)
		}
		if x.cfg.HealthCheck != nil {
			res := x.health.check(ctx, *x.cfg.HealthCheck, x.healthHost(), log)
			res.Target = name + " " + res.Target
			out.Health = append(out.Health, res)
			if !res.OK {
				return failure(fail(ReasonUnhealthy, "%s: health check failed: %s", name, res.Detail))
			}
		} else {
			out.Health = append(out.Health, HealthResult{Target: name, OK: true, Detail: "running", Attempts: 1, At: time.Now()})
		}
	}
	for _, rep := range done {
		if rep.hadPrevious {
			_ = x.docker.RemoveContainer(ctx, rep.name+"-previous")
		}
	}
	return out, nil
}

// revert removes the new containers and restarts the previous ones.
func (x *dockerExecutor) revert(ctx context.Context, done []replaced, log io.Writer) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	ok := len(done) > 0
	for i := len(done) - 1; i >= 0; i-- {
		rep := done[i]
		if err := x.docker.RemoveContainer(ctx, rep.name); err != nil {
			ok = false
			continue
		}
		if !rep.hadPrevious {
			continue
		}
		_, _ = fmt.Fprintf(log, "Restoring the previous %s\n", rep.name)
		if err := x.docker.RenameContainer(ctx, rep.name+"-previous", rep.name); err != nil {
			ok = false
			continue
		}
		if err := x.docker.StartContainer(ctx, rep.name); err != nil {
			ok = false
		}
	}
	return ok
}

func (x *dockerExecutor) Test(ctx context.Context) TestResult {
	version, platform, err := x.docker.Version(ctx)
	if err != nil {
		return TestResult{Checks: []Check{{Name: "docker", Detail: err.Error()}}}
	}
	res := TestResult{OK: true, Checks: []Check{{Name: "docker", OK: true, Detail: "Docker " + version + " " + platform}}}
	for _, name := range x.names() {
		info, err := x.docker.InspectContainer(ctx, name)
		switch {
		case errors.Is(err, dockerapi.ErrNotFound):
			res.Checks = append(res.Checks, Check{Name: name, OK: true, Detail: "not created yet"})
		case err != nil:
			res.OK = false
			res.Checks = append(res.Checks, Check{Name: name, Detail: err.Error()})
		default:
			res.Checks = append(res.Checks, Check{Name: name, OK: true, Detail: strings.TrimSpace(info.Status + " " + info.Image)})
		}
	}
	return res
}

func (x *dockerExecutor) Close() error {
	if x.ssh != nil {
		return x.ssh.Close()
	}
	return nil
}
