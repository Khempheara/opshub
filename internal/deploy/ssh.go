package deploy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// errHostKey means the server's key isn't pinned or changed; nothing was sent to it.
var errHostKey = errors.New("host key not trusted")

// sshDialer opens SSH connections to pinned hosts only.
type sshDialer struct {
	net  *net.Dialer
	user string
	auth []ssh.AuthMethod
}

func newSSHDialer(netDialer *net.Dialer, user string, creds SSHCredentials) (*sshDialer, error) {
	d := &sshDialer{net: netDialer, user: user}
	switch {
	case creds.PrivateKey != "":
		var signer ssh.Signer
		var err error
		if creds.Passphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(creds.PrivateKey), []byte(creds.Passphrase))
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(creds.PrivateKey))
		}
		if err != nil {
			return nil, fmt.Errorf("private key: %w", err)
		}
		d.auth = []ssh.AuthMethod{ssh.PublicKeys(signer)}
	case creds.Password != "":
		d.auth = []ssh.AuthMethod{ssh.Password(creds.Password)}
	default:
		return nil, errors.New("no SSH credentials")
	}
	return d, nil
}

// probe connects without authenticating and returns the server's key fingerprint.
func (d *sshDialer) probe(ctx context.Context, addr string) (string, error) {
	var fp string
	_, err := d.connect(ctx, addr, func(key ssh.PublicKey) error {
		fp = ssh.FingerprintSHA256(key)
		return errHostKey // stop before sending any credentials
	})
	if fp != "" {
		return fp, nil
	}
	return "", err
}

// dial connects to addr, requiring the server key to match pinned.
func (d *sshDialer) dial(ctx context.Context, addr, pinned string) (*ssh.Client, error) {
	if pinned == "" {
		return nil, errHostKey
	}
	return d.connect(ctx, addr, func(key ssh.PublicKey) error {
		if ssh.FingerprintSHA256(key) != pinned {
			return errHostKey
		}
		return nil
	})
}

func (d *sshDialer) connect(ctx context.Context, addr string, verify func(ssh.PublicKey) error) (*ssh.Client, error) {
	conn, err := d.net.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(15 * time.Second)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = conn.SetDeadline(deadline)
	cfg := &ssh.ClientConfig{
		User: d.user,
		Auth: d.auth,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			return verify(key)
		},
		Timeout: 15 * time.Second,
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		_ = conn.Close()
		if errors.Is(err, errHostKey) || strings.Contains(err.Error(), errHostKey.Error()) {
			return nil, errHostKey
		}
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return ssh.NewClient(c, chans, reqs), nil
}

// lockedWriter serializes writes: a session copies stdout and stderr in separate goroutines.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(b)
}

// runRemote executes script on the client, streaming output to log. The session is closed
// when ctx ends.
func runRemote(ctx context.Context, c *ssh.Client, script string, log io.Writer) error {
	sess, err := c.NewSession()
	if err != nil {
		return err
	}
	defer func() { _ = sess.Close() }()
	lw := &lockedWriter{w: log}
	sess.Stdout, sess.Stderr = lw, lw
	done := make(chan error, 1)
	go func() { done <- sess.Run(script) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		_ = sess.Signal(ssh.SIGTERM)
		_ = sess.Close()
		return ctx.Err()
	}
}

// prefixWriter prefixes every line with a tag ("[web-1] "). A session writes stdout and
// stderr from separate goroutines, so the buffer has its own lock; mu serializes output
// shared by several hosts.
type prefixWriter struct {
	mu     *sync.Mutex
	w      io.Writer
	prefix string
	own    sync.Mutex
	buf    bytes.Buffer
}

func (p *prefixWriter) Write(b []byte) (int, error) {
	p.own.Lock()
	defer p.own.Unlock()
	p.buf.Write(b)
	for {
		line, err := p.buf.ReadString('\n')
		if err != nil {
			p.buf.WriteString(line) // incomplete line: keep for later
			break
		}
		p.mu.Lock()
		_, _ = io.WriteString(p.w, p.prefix+line)
		p.mu.Unlock()
	}
	return len(b), nil
}

func (p *prefixWriter) flush() {
	p.own.Lock()
	defer p.own.Unlock()
	if p.buf.Len() > 0 {
		p.mu.Lock()
		_, _ = io.WriteString(p.w, p.prefix+p.buf.String()+"\n")
		p.mu.Unlock()
		p.buf.Reset()
	}
}

// sshExecutor runs the target's command on each host.
type sshExecutor struct {
	cfg    SSHConfig
	dialer *sshDialer
	health healthChecker
}

func hostOnly(addr string) string {
	h, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return h
}

func (x *sshExecutor) script(r Release, version, previous string) string {
	env := map[string]string{
		"OPSHUB_VERSION": version, "OPSHUB_PREVIOUS_VERSION": previous, "OPSHUB_ENVIRONMENT": r.Environment,
		"OPSHUB_PROJECT": r.Project, "OPSHUB_DEPLOYMENT_ID": r.DeploymentID.String(), "OPSHUB_STRATEGY": r.Strategy,
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "export %s=%s\n", k, shellQuote(env[k]))
	}
	b.WriteString("set -e\n")
	b.WriteString(x.cfg.Command)
	b.WriteString("\n")
	return b.String()
}

// apply runs the command for version on one host, then its health check.
func (x *sshExecutor) apply(ctx context.Context, r Release, host, version, previous string, log io.Writer, mu *sync.Mutex) (*HealthResult, error) {
	pw := &prefixWriter{mu: mu, w: log, prefix: "[" + host + "] "}
	defer pw.flush()
	c, err := x.dialer.dial(ctx, host, x.cfg.HostKeys[host])
	if errors.Is(err, errHostKey) {
		return nil, fail(ReasonHostKeyUntrusted, "%s: host key isn't trusted; run Test connection and trust it", host)
	}
	if err != nil {
		return nil, fail(ReasonUnreachable, "%s: %v", host, err)
	}
	defer func() { _ = c.Close() }()
	if err := runRemote(ctx, c, x.script(r, version, previous), pw); err != nil {
		return nil, fail(ReasonDeployFailed, "%s: command failed: %v", host, err)
	}
	pw.flush()
	if x.cfg.HealthCheck == nil {
		return nil, nil
	}
	res := x.health.check(ctx, *x.cfg.HealthCheck, hostOnly(host), pw)
	if !res.OK {
		return &res, fail(ReasonUnhealthy, "%s: health check failed: %s", host, res.Detail)
	}
	return &res, nil
}

func (x *sshExecutor) Deploy(ctx context.Context, r Release, log io.Writer) (Outcome, error) {
	var out Outcome
	var mu sync.Mutex
	var done []string
	var failure error
	for start := 0; start < len(x.cfg.Hosts) && failure == nil; start += x.cfg.BatchSize {
		batch := x.cfg.Hosts[start:min(start+x.cfg.BatchSize, len(x.cfg.Hosts))]
		_, _ = fmt.Fprintf(log, "Deploying %s to %s\n", r.Version, strings.Join(batch, ", "))
		var wg sync.WaitGroup
		errs := make([]error, len(batch))
		results := make([]*HealthResult, len(batch))
		for i, h := range batch {
			wg.Add(1)
			go func() {
				defer wg.Done()
				results[i], errs[i] = x.apply(ctx, r, h, r.Version, r.Previous, log, &mu)
			}()
		}
		wg.Wait()
		for i, h := range batch {
			if results[i] != nil {
				out.Health = append(out.Health, *results[i])
			}
			done = append(done, h)
			if errs[i] != nil && failure == nil {
				failure = errs[i]
			}
		}
	}
	if failure == nil {
		return out, nil
	}
	if r.Previous == "" {
		_, _ = fmt.Fprintln(log, "No previous release to revert to.")
		return out, failure
	}
	_, _ = fmt.Fprintf(log, "Reverting %s to %s\n", strings.Join(done, ", "), r.Previous)
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
	defer cancel()
	reverted := true
	for _, h := range done {
		if _, err := x.apply(rctx, r, h, r.Previous, r.Version, log, &mu); err != nil {
			_, _ = fmt.Fprintf(log, "Revert of %s failed: %v\n", h, err)
			reverted = false
		}
	}
	out.Reverted = reverted
	return out, failure
}

func (x *sshExecutor) Test(ctx context.Context) TestResult {
	res := TestResult{OK: true}
	for _, h := range x.cfg.Hosts {
		ch := Check{Name: h}
		fp, err := x.dialer.probe(ctx, h)
		switch {
		case err != nil:
			ch.Detail = err.Error()
		default:
			ch.Fingerprint = fp
			pinned := x.cfg.HostKeys[h]
			switch {
			case pinned == "":
				ch.Detail = "host key not trusted yet"
			case pinned != fp:
				ch.Detail = "host key changed since it was trusted"
			default:
				ch.Pinned = true
				c, err := x.dialer.dial(ctx, h, pinned)
				if err != nil {
					ch.Detail = "authentication failed: " + err.Error()
					break
				}
				var b bytes.Buffer
				err = runRemote(ctx, c, "echo ok", &b)
				_ = c.Close()
				if err != nil {
					ch.Detail = "command failed: " + err.Error()
					break
				}
				ch.OK = true
				ch.Detail = "connected as " + x.cfg.User
			}
		}
		res.OK = res.OK && ch.OK
		res.Checks = append(res.Checks, ch)
	}
	return res
}

func (x *sshExecutor) Close() error { return nil }
