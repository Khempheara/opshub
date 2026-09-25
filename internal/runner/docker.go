package runner

import (
	"bufio"
	"bytes"
	"context"
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

// Docker is a minimal Docker Engine API client over a unix socket (only what the executor
// needs, so the agent has no Docker SDK dependency).
type Docker struct {
	http    *http.Client
	version string // negotiated API version, e.g. "1.47"
}

// Supported API range: 1.41 (Docker 20.10) up to 1.47, the newest this client was tested with.
const (
	minAPIVersion = "1.41"
	maxAPIVersion = "1.47"
)

// ErrNotFound is returned for 404 answers (missing image, container or path).
var ErrNotFound = errors.New("not found")

// NewDocker connects to the daemon at socket and negotiates the API version.
func NewDocker(ctx context.Context, socket string) (*Docker, error) {
	d := &Docker{http: &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socket)
		},
		MaxIdleConns:    10,
		IdleConnTimeout: 30 * time.Second,
	}}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/_ping", nil)
	if err != nil {
		return nil, err
	}
	resp, err := d.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("connect to docker at %s: %w", socket, err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("docker ping: %s", resp.Status)
	}
	server := resp.Header.Get("Api-Version")
	switch {
	case server == "":
		return nil, errors.New("docker didn't report its API version")
	case versionLess(server, minAPIVersion):
		return nil, fmt.Errorf("docker API %s is too old (need %s or newer)", server, minAPIVersion)
	case versionLess(server, maxAPIVersion):
		d.version = server
	default:
		d.version = maxAPIVersion
	}
	return d, nil
}

func versionLess(a, b string) bool {
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

func (d *Docker) do(ctx context.Context, method, path string, query url.Values, body io.Reader, contentType string) (*http.Response, error) {
	u := "http://docker/v" + d.version + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := d.http.Do(req)
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

func (d *Docker) doJSON(ctx context.Context, method, path string, query url.Values, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	resp, err := d.do(ctx, method, path, query, body, "application/json")
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

// EnsureImage pulls image unless it is present (or always, when always is true). Progress
// lines are written to progress.
func (d *Docker) EnsureImage(ctx context.Context, image string, always bool, progress io.Writer) error {
	if !always {
		if err := d.doJSON(ctx, http.MethodGet, "/images/"+url.PathEscape(image)+"/json", nil, nil, nil); err == nil {
			return nil
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
	}
	ref, tag := splitImage(image)
	resp, err := d.do(ctx, http.MethodPost, "/images/create", url.Values{"fromImage": {ref}, "tag": {tag}}, nil, "")
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
		// Per-layer progress is noise in a job log; keep the summary lines.
		if m.ID == "" || strings.HasPrefix(m.Status, "Pulling from") {
			if line := strings.TrimSpace(m.Status); line != "" && line != last {
				_, _ = fmt.Fprintln(progress, line)
				last = line
			}
		}
	}
}

// splitImage separates "repo:tag" (the tag defaults to latest; digests pass through).
func splitImage(image string) (string, string) {
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
func (d *Docker) CreateVolume(ctx context.Context, name string, labels map[string]string) error {
	return d.doJSON(ctx, http.MethodPost, "/volumes/create", nil, map[string]any{"Name": name, "Labels": labels}, nil)
}

// RemoveVolume removes a volume (missing volumes are fine).
func (d *Docker) RemoveVolume(ctx context.Context, name string) error {
	err := d.doJSON(ctx, http.MethodDelete, "/volumes/"+url.PathEscape(name), url.Values{"force": {"1"}}, nil, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// ContainerSpec is the subset of container settings the executor uses.
type ContainerSpec struct {
	Name       string
	Image      string
	Entrypoint []string
	Cmd        []string
	Env        []string
	WorkingDir string
	Labels     map[string]string
	Volume     string // mounted at MountPath
	MountPath  string
	NanoCPUs   int64
	Memory     int64
	PidsLimit  int64
	Network    string
}

// CreateContainer creates (doesn't start) a container and returns its id. Containers are
// never privileged and can't gain privileges (no-new-privileges).
func (d *Docker) CreateContainer(ctx context.Context, s ContainerSpec) (string, error) {
	host := map[string]any{
		"Binds":       []string{s.Volume + ":" + s.MountPath},
		"Privileged":  false,
		"SecurityOpt": []string{"no-new-privileges"},
		"Init":        true,
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
	body := map[string]any{
		"Image": s.Image, "Cmd": s.Cmd, "Env": s.Env, "WorkingDir": s.WorkingDir, "Labels": s.Labels,
		"Entrypoint": s.Entrypoint, "Tty": false, "AttachStdout": true, "AttachStderr": true, "HostConfig": host,
	}
	var out struct {
		ID string `json:"Id"`
	}
	err := d.doJSON(ctx, http.MethodPost, "/containers/create", url.Values{"name": {s.Name}}, body, &out)
	return out.ID, err
}

// StartContainer starts a created container.
func (d *Docker) StartContainer(ctx context.Context, id string) error {
	return d.doJSON(ctx, http.MethodPost, "/containers/"+id+"/start", nil, nil, nil)
}

// FollowLogs streams a container's stdout and stderr (demultiplexed) to w until it exits.
func (d *Docker) FollowLogs(ctx context.Context, id string, w io.Writer) error {
	resp, err := d.do(ctx, http.MethodGet, "/containers/"+id+"/logs",
		url.Values{"follow": {"1"}, "stdout": {"1"}, "stderr": {"1"}}, nil, "")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	return demux(resp.Body, w)
}

// demux copies Docker's multiplexed stream format (8-byte frame headers) to w.
func demux(r io.Reader, w io.Writer) error {
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
func (d *Docker) WaitContainer(ctx context.Context, id string) (int, error) {
	var out struct {
		StatusCode int `json:"StatusCode"`
		Error      *struct {
			Message string `json:"Message"`
		} `json:"Error"`
	}
	if err := d.doJSON(ctx, http.MethodPost, "/containers/"+id+"/wait", url.Values{"condition": {"not-running"}}, nil, &out); err != nil {
		return -1, err
	}
	if out.Error != nil && out.Error.Message != "" {
		return out.StatusCode, errors.New(out.Error.Message)
	}
	return out.StatusCode, nil
}

// KillContainer stops a container immediately (already stopped is fine).
func (d *Docker) KillContainer(ctx context.Context, id string) error {
	err := d.doJSON(ctx, http.MethodPost, "/containers/"+id+"/kill", nil, nil, nil)
	if err != nil && (errors.Is(err, ErrNotFound) || strings.Contains(err.Error(), "is not running")) {
		return nil
	}
	return err
}

// RemoveContainer force-removes a container.
func (d *Docker) RemoveContainer(ctx context.Context, id string) error {
	err := d.doJSON(ctx, http.MethodDelete, "/containers/"+id, url.Values{"force": {"1"}, "v": {"1"}}, nil, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// PutArchive extracts a tar stream at path inside the container (which may be stopped).
func (d *Docker) PutArchive(ctx context.Context, id, path string, tarStream io.Reader) error {
	resp, err := d.do(ctx, http.MethodPut, "/containers/"+id+"/archive", url.Values{"path": {path}}, tarStream, "application/x-tar")
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.Body.Close()
}

// GetArchive returns a tar stream of path inside the container. Entries are named after
// path's last element. ErrNotFound when path doesn't exist.
func (d *Docker) GetArchive(ctx context.Context, id, path string) (io.ReadCloser, error) {
	resp, err := d.do(ctx, http.MethodGet, "/containers/"+id+"/archive", url.Values{"path": {path}}, nil, "")
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// ListContainers returns the ids of containers (running or not) carrying label.
func (d *Docker) ListContainers(ctx context.Context, label string) ([]string, error) {
	filters, _ := json.Marshal(map[string][]string{"label": {label}})
	var out []struct {
		ID string `json:"Id"`
	}
	if err := d.doJSON(ctx, http.MethodGet, "/containers/json", url.Values{"all": {"1"}, "filters": {string(filters)}}, nil, &out); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(out))
	for _, c := range out {
		ids = append(ids, c.ID)
	}
	return ids, nil
}

// ListVolumes returns the names of volumes carrying label.
func (d *Docker) ListVolumes(ctx context.Context, label string) ([]string, error) {
	filters, _ := json.Marshal(map[string][]string{"label": {label}})
	var out struct {
		Volumes []struct {
			Name string `json:"Name"`
		} `json:"Volumes"`
	}
	if err := d.doJSON(ctx, http.MethodGet, "/volumes", url.Values{"filters": {string(filters)}}, nil, &out); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(out.Volumes))
	for _, v := range out.Volumes {
		names = append(names, v.Name)
	}
	return names, nil
}
