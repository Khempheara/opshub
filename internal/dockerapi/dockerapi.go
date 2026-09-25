// Package dockerapi is a minimal Docker Engine API client (only what OpsHub needs), used by
// the runner agent's executor and by Docker deploy targets. It speaks HTTP over any
// connection — a unix socket, TCP with TLS, or a channel through SSH — so there is no Docker
// SDK dependency.
package dockerapi

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Supported API range: 1.41 (Docker 20.10) up to 1.47, the newest this client was tested with.
const (
	minAPIVersion = "1.41"
	maxAPIVersion = "1.47"
)

// ErrNotFound is returned for 404 answers (missing image, container or path).
var ErrNotFound = errors.New("not found")

// DialFunc opens a connection to the daemon.
type DialFunc func(ctx context.Context) (net.Conn, error)

// Client talks to one Docker daemon.
type Client struct {
	http    *http.Client
	scheme  string
	version string // negotiated API version, e.g. "1.47"
	auth    string // X-Registry-Auth for pulls, base64url JSON; "" = none
}

// Registry credentials for pulling private images.
type Registry struct {
	Server   string `json:"serveraddress,omitempty"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// New connects through dial (plain HTTP, or HTTPS when tlsConfig is set) and negotiates the
// API version.
func New(ctx context.Context, dial DialFunc, tlsConfig *tls.Config) (*Client, error) {
	tr := &http.Transport{
		DialContext:     func(ctx context.Context, _, _ string) (net.Conn, error) { return dial(ctx) },
		MaxIdleConns:    10,
		IdleConnTimeout: 30 * time.Second,
		Proxy:           nil,
	}
	c := &Client{http: &http.Client{Transport: tr}, scheme: "http"}
	if tlsConfig != nil {
		tr.TLSClientConfig = tlsConfig
		c.scheme = "https"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.scheme+"://docker/_ping", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("connect to docker: %w", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("docker ping: %s", resp.Status)
	}
	server := resp.Header.Get("Api-Version")
	switch {
	case server == "":
		return nil, errors.New("docker didn't report its API version")
	case VersionLess(server, minAPIVersion):
		return nil, fmt.Errorf("docker API %s is too old (need %s or newer)", server, minAPIVersion)
	case VersionLess(server, maxAPIVersion):
		c.version = server
	default:
		c.version = maxAPIVersion
	}
	return c, nil
}

// NewUnix connects to the daemon's unix socket.
func NewUnix(ctx context.Context, socket string) (*Client, error) {
	c, err := New(ctx, func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", socket)
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("docker at %s: %w", socket, err)
	}
	return c, nil
}

// WithRegistry returns a copy of the client that sends registry credentials on pulls.
func (c *Client) WithRegistry(r Registry) *Client {
	b, _ := json.Marshal(r) // #nosec G117 -- the Engine API takes registry credentials in X-Registry-Auth
	cp := *c
	cp.auth = base64.URLEncoding.EncodeToString(b)
	return &cp
}

// APIVersion is the negotiated API version.
func (c *Client) APIVersion() string { return c.version }

// VersionLess compares "major.minor" versions.
func VersionLess(a, b string) bool {
	parse := func(s string) (int, int) {
		maj, minor, _ := strings.Cut(s, ".")
		x, _ := strconv.Atoi(maj)
		y, _ := strconv.Atoi(minor)
		return x, y
	}
	am, an := parse(a)
	bm, bn := parse(b)
	return am < bm || (am == bm && an < bn)
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body io.Reader, contentType string, header http.Header) (*http.Response, error) {
	u := c.scheme + "://docker/v" + c.version + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	for k, v := range header {
		req.Header[k] = v
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		defer func() { _ = resp.Body.Close() }()
		var msg struct {
			Message string `json:"message"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&msg)
		if resp.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, msg.Message)
		}
		return nil, fmt.Errorf("docker %s %s: %s: %s", method, path, resp.Status, msg.Message)
	}
	return resp, nil
}

func (c *Client) doJSON(ctx context.Context, method, path string, query url.Values, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	resp, err := c.do(ctx, method, path, query, body, "application/json", nil)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Version reports the daemon's version and platform.
func (c *Client) Version(ctx context.Context) (string, string, error) {
	var out struct {
		Version string `json:"Version"`
		Os      string `json:"Os"`
		Arch    string `json:"Arch"`
	}
	err := c.doJSON(ctx, http.MethodGet, "/version", nil, nil, &out)
	return out.Version, out.Os + "/" + out.Arch, err
}

// EnsureImage pulls image unless it is present (or always, when always is true). Summary
// progress lines are written to progress.
func (c *Client) EnsureImage(ctx context.Context, image string, always bool, progress io.Writer) error {
	if !always {
		if err := c.doJSON(ctx, http.MethodGet, "/images/"+url.PathEscape(image)+"/json", nil, nil, nil); err == nil {
			return nil
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
	}
	ref, tag := SplitImage(image)
	var h http.Header
	if c.auth != "" {
		h = http.Header{"X-Registry-Auth": {c.auth}}
	}
	resp, err := c.do(ctx, http.MethodPost, "/images/create", url.Values{"fromImage": {ref}, "tag": {tag}}, nil, "", h)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	// The pull reports errors inside its JSON progress stream.
	dec := json.NewDecoder(resp.Body)
	last := ""
	for {
		var m struct {
			Status string `json:"status"`
			ID     string `json:"id"`
			Error  string `json:"error"`
		}
		if err := dec.Decode(&m); errors.Is(err, io.EOF) {
			return nil
		} else if err != nil {
			return err
		}
		if m.Error != "" {
			return fmt.Errorf("pull %s: %s", image, m.Error)
		}
		// Per-layer progress is noise in a log; keep the summary lines.
		if m.ID == "" || strings.HasPrefix(m.Status, "Pulling from") {
			if line := strings.TrimSpace(m.Status); line != "" && line != last {
				_, _ = fmt.Fprintln(progress, line)
				last = line
			}
		}
	}
}

// SplitImage separates "repo:tag" (the tag defaults to latest; digests pass through).
func SplitImage(image string) (string, string) {
	if strings.Contains(image, "@") {
		return image, ""
	}
	slash := strings.LastIndex(image, "/")
	if colon := strings.LastIndex(image, ":"); colon > slash {
		return image[:colon], image[colon+1:]
	}
	return image, "latest"
}

// CreateVolume creates a named volume with labels.
func (c *Client) CreateVolume(ctx context.Context, name string, labels map[string]string) error {
	return c.doJSON(ctx, http.MethodPost, "/volumes/create", nil, map[string]any{"Name": name, "Labels": labels}, nil)
}

// RemoveVolume removes a volume (missing volumes are fine).
func (c *Client) RemoveVolume(ctx context.Context, name string) error {
	err := c.doJSON(ctx, http.MethodDelete, "/volumes/"+url.PathEscape(name), url.Values{"force": {"1"}}, nil, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// ContainerSpec is the subset of container settings OpsHub uses.
type ContainerSpec struct {
	Name       string
	Image      string
	Entrypoint []string // nil keeps the image's entrypoint
	Cmd        []string // nil keeps the image's command
	Env        []string
	WorkingDir string
	Labels     map[string]string
	Binds      []string // "volume:/path"
	Ports      []string // "8080:80" or "127.0.0.1:8080:80" (host:container, TCP)
	Restart    string   // "", "always", "unless-stopped", "on-failure"
	NanoCPUs   int64
	Memory     int64
	PidsLimit  int64
	Network    string
	Init       bool
}

// portBindings converts "[ip:]host:container" specs to Docker's structures.
func portBindings(specs []string) (map[string]any, map[string][]map[string]string, error) {
	exposed := map[string]any{}
	bindings := map[string][]map[string]string{}
	for _, s := range specs {
		parts := strings.Split(s, ":")
		var ip, host, ctr string
		switch len(parts) {
		case 2:
			host, ctr = parts[0], parts[1]
		case 3:
			ip, host, ctr = parts[0], parts[1], parts[2]
		default:
			return nil, nil, fmt.Errorf("invalid port mapping %q", s)
		}
		key := ctr + "/tcp"
		exposed[key] = struct{}{}
		b := map[string]string{"HostPort": host}
		if ip != "" {
			b["HostIp"] = ip
		}
		bindings[key] = append(bindings[key], b)
	}
	return exposed, bindings, nil
}

// CreateContainer creates (doesn't start) a container and returns its id. Containers are
// never privileged and can't gain privileges (no-new-privileges).
func (c *Client) CreateContainer(ctx context.Context, s ContainerSpec) (string, error) {
	host := map[string]any{
		"Privileged":  false,
		"SecurityOpt": []string{"no-new-privileges"},
		"Init":        s.Init,
	}
	if len(s.Binds) > 0 {
		host["Binds"] = s.Binds
	}
	if s.NanoCPUs > 0 {
		host["NanoCpus"] = s.NanoCPUs
	}
	if s.Memory > 0 {
		host["Memory"] = s.Memory
		host["MemorySwap"] = s.Memory
	}
	if s.PidsLimit > 0 {
		host["PidsLimit"] = s.PidsLimit
	}
	if s.Network != "" {
		host["NetworkMode"] = s.Network
	}
	if s.Restart != "" {
		host["RestartPolicy"] = map[string]any{"Name": s.Restart}
	}
	body := map[string]any{
		"Image": s.Image, "Env": s.Env, "WorkingDir": s.WorkingDir, "Labels": s.Labels,
		"Tty": false, "AttachStdout": true, "AttachStderr": true,
	}
	if s.Entrypoint != nil {
		body["Entrypoint"] = s.Entrypoint
	}
	if s.Cmd != nil {
		body["Cmd"] = s.Cmd
	}
	if len(s.Ports) > 0 {
		exposed, bindings, err := portBindings(s.Ports)
		if err != nil {
			return "", err
		}
		body["ExposedPorts"] = exposed
		host["PortBindings"] = bindings
	}
	body["HostConfig"] = host
	var out struct {
		ID string `json:"Id"`
	}
	err := c.doJSON(ctx, http.MethodPost, "/containers/create", url.Values{"name": {s.Name}}, body, &out)
	return out.ID, err
}

// StartContainer starts a created or stopped container.
func (c *Client) StartContainer(ctx context.Context, id string) error {
	err := c.doJSON(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/start", nil, nil, nil)
	if err != nil && strings.Contains(err.Error(), "304") {
		return nil // already started
	}
	return err
}

// StopContainer stops a container, killing it after timeout.
func (c *Client) StopContainer(ctx context.Context, id string, timeout time.Duration) error {
	err := c.doJSON(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/stop",
		url.Values{"t": {strconv.Itoa(int(timeout / time.Second))}}, nil, nil)
	if err != nil && strings.Contains(err.Error(), "304") {
		return nil // already stopped
	}
	return err
}

// RenameContainer renames a container.
func (c *Client) RenameContainer(ctx context.Context, id, name string) error {
	return c.doJSON(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/rename", url.Values{"name": {name}}, nil, nil)
}

// ContainerInfo is the part of an inspection the deployer needs.
type ContainerInfo struct {
	ID      string
	Name    string
	Image   string // the image reference it was created from
	Running bool
	Status  string // created, running, exited, …
	Health  string // "" when the image defines no HEALTHCHECK; starting, healthy, unhealthy
	Exit    int
}

// InspectContainer describes a container by id or name (ErrNotFound when missing).
func (c *Client) InspectContainer(ctx context.Context, idOrName string) (ContainerInfo, error) {
	var out struct {
		ID     string `json:"Id"`
		Name   string `json:"Name"`
		Config struct {
			Image string `json:"Image"`
		} `json:"Config"`
		State struct {
			Status   string `json:"Status"`
			Running  bool   `json:"Running"`
			ExitCode int    `json:"ExitCode"`
			Health   *struct {
				Status string `json:"Status"`
			} `json:"Health"`
		} `json:"State"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/containers/"+url.PathEscape(idOrName)+"/json", nil, nil, &out); err != nil {
		return ContainerInfo{}, err
	}
	info := ContainerInfo{
		ID: out.ID, Name: strings.TrimPrefix(out.Name, "/"), Image: out.Config.Image,
		Running: out.State.Running, Status: out.State.Status, Exit: out.State.ExitCode,
	}
	if out.State.Health != nil {
		info.Health = out.State.Health.Status
	}
	return info, nil
}

// FollowLogs streams a container's stdout and stderr (demultiplexed) to w until it exits.
func (c *Client) FollowLogs(ctx context.Context, id string, w io.Writer) error {
	resp, err := c.do(ctx, http.MethodGet, "/containers/"+url.PathEscape(id)+"/logs",
		url.Values{"follow": {"1"}, "stdout": {"1"}, "stderr": {"1"}}, nil, "", nil)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	return Demux(resp.Body, w)
}

// Logs returns up to tail lines of a container's output (demultiplexed).
func (c *Client) Logs(ctx context.Context, id string, tail int) (string, error) {
	resp, err := c.do(ctx, http.MethodGet, "/containers/"+url.PathEscape(id)+"/logs",
		url.Values{"stdout": {"1"}, "stderr": {"1"}, "tail": {strconv.Itoa(tail)}}, nil, "", nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	var b bytes.Buffer
	err = Demux(io.LimitReader(resp.Body, 256<<10), &b)
	return b.String(), err
}

// Demux copies Docker's multiplexed stream format (8-byte frame headers) to w.
func Demux(r io.Reader, w io.Writer) error {
	br := bufio.NewReaderSize(r, 32<<10)
	var hdr [8]byte
	for {
		if _, err := io.ReadFull(br, hdr[:]); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return nil
			}
			return err
		}
		size := int64(binary.BigEndian.Uint32(hdr[4:]))
		if _, err := io.CopyN(w, br, size); err != nil {
			return err
		}
	}
}

// WaitContainer blocks until the container exits and returns its exit code.
func (c *Client) WaitContainer(ctx context.Context, id string) (int, error) {
	var out struct {
		StatusCode int `json:"StatusCode"`
		Error      *struct {
			Message string `json:"Message"`
		} `json:"Error"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/wait", url.Values{"condition": {"not-running"}}, nil, &out); err != nil {
		return -1, err
	}
	if out.Error != nil && out.Error.Message != "" {
		return out.StatusCode, errors.New(out.Error.Message)
	}
	return out.StatusCode, nil
}

// KillContainer stops a container immediately (already stopped is fine).
func (c *Client) KillContainer(ctx context.Context, id string) error {
	err := c.doJSON(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/kill", nil, nil, nil)
	if err != nil && (errors.Is(err, ErrNotFound) || strings.Contains(err.Error(), "is not running")) {
		return nil
	}
	return err
}

// RemoveContainer force-removes a container (missing containers are fine).
func (c *Client) RemoveContainer(ctx context.Context, id string) error {
	err := c.doJSON(ctx, http.MethodDelete, "/containers/"+url.PathEscape(id), url.Values{"force": {"1"}, "v": {"1"}}, nil, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// PutArchive extracts a tar stream at path inside the container (which may be stopped).
func (c *Client) PutArchive(ctx context.Context, id, path string, tarStream io.Reader) error {
	resp, err := c.do(ctx, http.MethodPut, "/containers/"+url.PathEscape(id)+"/archive", url.Values{"path": {path}}, tarStream, "application/x-tar", nil)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.Body.Close()
}

// GetArchive returns a tar stream of path inside the container. Entries are named after
// path's last element. ErrNotFound when path doesn't exist.
func (c *Client) GetArchive(ctx context.Context, id, path string) (io.ReadCloser, error) {
	resp, err := c.do(ctx, http.MethodGet, "/containers/"+url.PathEscape(id)+"/archive", url.Values{"path": {path}}, nil, "", nil)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// ListContainers returns the ids of containers (running or not) carrying label.
func (c *Client) ListContainers(ctx context.Context, label string) ([]string, error) {
	filters, _ := json.Marshal(map[string][]string{"label": {label}})
	var out []struct {
		ID string `json:"Id"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/containers/json", url.Values{"all": {"1"}, "filters": {string(filters)}}, nil, &out); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(out))
	for _, o := range out {
		ids = append(ids, o.ID)
	}
	return ids, nil
}

// ListVolumes returns the names of volumes carrying label.
func (c *Client) ListVolumes(ctx context.Context, label string) ([]string, error) {
	filters, _ := json.Marshal(map[string][]string{"label": {label}})
	var out struct {
		Volumes []struct {
			Name string `json:"Name"`
		} `json:"Volumes"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/volumes", url.Values{"filters": {string(filters)}}, nil, &out); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(out.Volumes))
	for _, v := range out.Volumes {
		names = append(names, v.Name)
	}
	return names, nil
}
