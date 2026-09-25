package deploy

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/store"
)

// Kinds of deploy target.
const (
	KindSSH        = store.DeployTargetKindSsh
	KindDocker     = store.DeployTargetKindDocker
	KindKubernetes = store.DeployTargetKindKubernetes
)

// Docker connections.
const (
	DockerViaSSH   = "ssh"
	DockerViaTLS   = "tls"
	DockerViaLocal = "local" // the API host's own socket; operators opt in
)

// HealthCheck is an HTTP check run after a release is applied. {host} in the URL is replaced
// by each host's name for SSH targets and Docker-over-SSH.
type HealthCheck struct {
	URL            string `json:"url"`
	ExpectedStatus int    `json:"expected_status,omitempty"` // 0 = any 2xx
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"` // default 60
}

// SSHConfig deploys by running Command on every host, in batches.
type SSHConfig struct {
	Hosts       []string          `json:"hosts"` // host or host:port (default 22)
	User        string            `json:"user"`
	HostKeys    map[string]string `json:"host_keys,omitempty"` // host:port → SHA256 fingerprint
	Command     string            `json:"command"`
	BatchSize   int               `json:"batch_size,omitempty"` // hosts at a time, default 1
	HealthCheck *HealthCheck      `json:"health_check,omitempty"`
}

// SSHCredentials authenticate to the hosts (a key, or a password).
type SSHCredentials struct {
	PrivateKey string `json:"private_key,omitempty"`
	Passphrase string `json:"passphrase,omitempty"`
	Password   string `json:"password,omitempty"`
}

// DockerConfig replaces one or more containers with the new image.
type DockerConfig struct {
	Connection  string            `json:"connection"`         // ssh | tls | local
	Host        string            `json:"host,omitempty"`     // ssh: host[:22]; tls: host:port
	User        string            `json:"user,omitempty"`     // ssh
	HostKey     string            `json:"host_key,omitempty"` // ssh: pinned SHA256 fingerprint
	Socket      string            `json:"socket,omitempty"`   // ssh/local, default /var/run/docker.sock
	Container   string            `json:"container"`
	Replicas    int               `json:"replicas,omitempty"` // default 1
	Ports       []string          `json:"ports,omitempty"`    // [ip:]host:container (replicas = 1 only)
	Env         map[string]string `json:"env,omitempty"`
	Network     string            `json:"network,omitempty"`
	Restart     string            `json:"restart,omitempty"` // default unless-stopped
	HealthCheck *HealthCheck      `json:"health_check,omitempty"`
}

// DockerCredentials hold the connection's secrets and optional registry credentials.
type DockerCredentials struct {
	PrivateKey       string `json:"private_key,omitempty"`
	Passphrase       string `json:"passphrase,omitempty"`
	CACert           string `json:"ca_cert,omitempty"`
	ClientCert       string `json:"client_cert,omitempty"`
	ClientKey        string `json:"client_key,omitempty"`
	RegistryServer   string `json:"registry_server,omitempty"`
	RegistryUsername string `json:"registry_username,omitempty"`
	RegistryPassword string `json:"registry_password,omitempty"`
}

// KubernetesConfig updates a Deployment's image (rolling) or switches a Service between
// <deployment>-blue and <deployment>-green (blue/green).
type KubernetesConfig struct {
	Namespace             string       `json:"namespace"`
	Deployment            string       `json:"deployment"`
	Container             string       `json:"container,omitempty"` // default: the first container
	Service               string       `json:"service,omitempty"`   // required for blue/green
	RolloutTimeoutSeconds int          `json:"rollout_timeout_seconds,omitempty"`
	HealthCheck           *HealthCheck `json:"health_check,omitempty"`
}

// KubernetesCredentials is a kubeconfig with a token or client certificate (no exec plugins).
type KubernetesCredentials struct {
	Kubeconfig string `json:"kubeconfig"`
}

var (
	namePattern    = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9_-]{0,38}[a-z0-9])?$`)
	userPattern    = regexp.MustCompile(`^[a-z_][a-z0-9_.-]{0,31}$`)
	k8sNamePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)
	ctrNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,62}$`)
	envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
	portPattern    = regexp.MustCompile(`^(?:(?:\d{1,3}\.){3}\d{1,3}:)?\d{1,5}:\d{1,5}$`)
	fpPattern      = regexp.MustCompile(`^SHA256:[A-Za-z0-9+/]{43}$`)
	// ImagePattern accepts registry/path:tag and @sha256 digests.
	ImagePattern = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*(?:\.[a-z0-9-]+)*(?::[0-9]{1,5})?(?:/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*(?::[A-Za-z0-9_][A-Za-z0-9._-]{0,127})?(?:@sha256:[a-f0-9]{64})?$`)
)

type fieldErrors []apperr.FieldError

func (f *fieldErrors) add(field, rule, param string) {
	*f = append(*f, apperr.FieldError{Field: field, Rule: rule, Param: param})
}

func (f fieldErrors) err() error {
	if len(f) == 0 {
		return nil
	}
	return apperr.Validation(f)
}

// strictDecode decodes raw into v, rejecting unknown fields.
func strictDecode(raw json.RawMessage, v any, field string) error {
	if len(raw) == 0 || string(raw) == "null" {
		raw = []byte("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return apperr.Validation([]apperr.FieldError{{Field: field, Rule: "type", Param: err.Error()}})
	}
	return nil
}

// hostPort adds the default port and validates host[:port].
func hostPort(s string, defPort int) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsAny(s, " /@") {
		return "", false
	}
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		host, port = s, strconv.Itoa(defPort)
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 || host == "" {
		return "", false
	}
	return net.JoinHostPort(host, port), true
}

func (h *HealthCheck) validate(fe *fieldErrors, field string) {
	if h == nil {
		return
	}
	u, err := url.Parse(strings.ReplaceAll(h.URL, "{host}", "host"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		fe.add(field+".url", "url", "")
	}
	if h.ExpectedStatus != 0 && (h.ExpectedStatus < 100 || h.ExpectedStatus > 599) {
		fe.add(field+".expected_status", "range", "100-599")
	}
	if h.TimeoutSeconds < 0 || h.TimeoutSeconds > 900 {
		fe.add(field+".timeout_seconds", "range", "0-900")
	}
}

// normalize validates and fills defaults; it returns the normalized config JSON.
func (c *SSHConfig) normalize() (json.RawMessage, error) {
	var fe fieldErrors
	if len(c.Hosts) == 0 || len(c.Hosts) > 50 {
		fe.add("config.hosts", "range", "1-50")
	}
	for i, h := range c.Hosts {
		hp, ok := hostPort(h, 22)
		if !ok {
			fe.add(fmt.Sprintf("config.hosts[%d]", i), "host", "")
			continue
		}
		c.Hosts[i] = hp
	}
	if !userPattern.MatchString(c.User) {
		fe.add("config.user", "pattern", "")
	}
	pinned := map[string]string{}
	for h, fp := range c.HostKeys {
		hp, ok := hostPort(h, 22)
		if !ok || !fpPattern.MatchString(fp) {
			fe.add("config.host_keys", "fingerprint", "")
			continue
		}
		pinned[hp] = fp
	}
	c.HostKeys = pinned
	if strings.TrimSpace(c.Command) == "" || len(c.Command) > 4000 {
		fe.add("config.command", "range", "1-4000")
	}
	if c.BatchSize == 0 {
		c.BatchSize = 1
	}
	if c.BatchSize < 1 || c.BatchSize > 50 {
		fe.add("config.batch_size", "range", "1-50")
	}
	c.HealthCheck.validate(&fe, "config.health_check")
	if err := fe.err(); err != nil {
		return nil, err
	}
	return json.Marshal(c)
}

func (c SSHCredentials) validate(required bool) error {
	var fe fieldErrors
	switch {
	case c.PrivateKey != "":
		var err error
		if c.Passphrase != "" {
			_, err = ssh.ParsePrivateKeyWithPassphrase([]byte(c.PrivateKey), []byte(c.Passphrase))
		} else {
			_, err = ssh.ParsePrivateKey([]byte(c.PrivateKey))
		}
		if err != nil {
			fe.add("credentials.private_key", "private_key", "")
		}
	case c.Password != "":
	case required:
		fe.add("credentials.private_key", "required_without", "password")
	}
	return fe.err()
}

func (c *DockerConfig) normalize(allowLocal bool) (json.RawMessage, error) {
	var fe fieldErrors
	switch c.Connection {
	case DockerViaSSH:
		hp, ok := hostPort(c.Host, 22)
		if !ok {
			fe.add("config.host", "host", "")
		}
		c.Host = hp
		if !userPattern.MatchString(c.User) {
			fe.add("config.user", "pattern", "")
		}
		if c.HostKey != "" && !fpPattern.MatchString(c.HostKey) {
			fe.add("config.host_key", "fingerprint", "")
		}
	case DockerViaTLS:
		hp, ok := hostPort(c.Host, 2376)
		if !ok {
			fe.add("config.host", "host", "")
		}
		c.Host, c.User, c.HostKey = hp, "", ""
	case DockerViaLocal:
		if !allowLocal {
			fe.add("config.connection", "local_docker_disabled", "")
		}
		c.Host, c.User, c.HostKey = "", "", ""
	default:
		fe.add("config.connection", "oneof", "ssh tls local")
	}
	if c.Connection != DockerViaTLS {
		if c.Socket == "" {
			c.Socket = "/var/run/docker.sock"
		}
		if !strings.HasPrefix(c.Socket, "/") || strings.ContainsAny(c.Socket, " \n") {
			fe.add("config.socket", "path", "")
		}
	} else {
		c.Socket = ""
	}
	if !ctrNamePattern.MatchString(c.Container) {
		fe.add("config.container", "pattern", "")
	}
	if c.Replicas == 0 {
		c.Replicas = 1
	}
	if c.Replicas < 1 || c.Replicas > 20 {
		fe.add("config.replicas", "range", "1-20")
	}
	for i, p := range c.Ports {
		if !portPattern.MatchString(p) {
			fe.add(fmt.Sprintf("config.ports[%d]", i), "port", "")
		}
	}
	if len(c.Ports) > 0 && c.Replicas > 1 {
		fe.add("config.ports", "ports_need_one_replica", "")
	}
	if len(c.Env) > 100 {
		fe.add("config.env", "max", "100")
	}
	for k, v := range c.Env {
		if !envNamePattern.MatchString(k) || len(v) > 4096 {
			fe.add("config.env."+k, "variable_name", "")
		}
	}
	if c.Network != "" && !ctrNamePattern.MatchString(c.Network) {
		fe.add("config.network", "pattern", "")
	}
	if c.Restart == "" {
		c.Restart = "unless-stopped"
	}
	if c.Restart != "no" && c.Restart != "always" && c.Restart != "unless-stopped" && c.Restart != "on-failure" {
		fe.add("config.restart", "oneof", "no always unless-stopped on-failure")
	}
	c.HealthCheck.validate(&fe, "config.health_check")
	if err := fe.err(); err != nil {
		return nil, err
	}
	return json.Marshal(c)
}

func (c DockerCredentials) validate(connection string, required bool) error {
	var fe fieldErrors
	switch connection {
	case DockerViaSSH:
		if err := (SSHCredentials{PrivateKey: c.PrivateKey, Passphrase: c.Passphrase}).validate(required); err != nil {
			fe.add("credentials.private_key", "private_key", "")
		}
	case DockerViaTLS:
		if c.CACert == "" && c.ClientCert == "" && required {
			fe.add("credentials.client_cert", "required", "")
		}
		if c.CACert != "" && !x509.NewCertPool().AppendCertsFromPEM([]byte(c.CACert)) {
			fe.add("credentials.ca_cert", "certificate", "")
		}
		if c.ClientCert != "" || c.ClientKey != "" {
			if _, err := tls.X509KeyPair([]byte(c.ClientCert), []byte(c.ClientKey)); err != nil {
				fe.add("credentials.client_cert", "certificate", "")
			}
		}
	}
	if (c.RegistryUsername == "") != (c.RegistryPassword == "") {
		fe.add("credentials.registry_password", "required_with", "registry_username")
	}
	return fe.err()
}

func (c *KubernetesConfig) normalize() (json.RawMessage, error) {
	var fe fieldErrors
	if c.Namespace == "" {
		c.Namespace = "default"
	}
	if !k8sNamePattern.MatchString(c.Namespace) {
		fe.add("config.namespace", "pattern", "")
	}
	if !k8sNamePattern.MatchString(c.Deployment) || len(c.Deployment) > 57 { // room for "-green"
		fe.add("config.deployment", "pattern", "")
	}
	if c.Container != "" && !k8sNamePattern.MatchString(c.Container) {
		fe.add("config.container", "pattern", "")
	}
	if c.Service != "" && !k8sNamePattern.MatchString(c.Service) {
		fe.add("config.service", "pattern", "")
	}
	if c.RolloutTimeoutSeconds == 0 {
		c.RolloutTimeoutSeconds = 300
	}
	if c.RolloutTimeoutSeconds < 10 || c.RolloutTimeoutSeconds > 3600 {
		fe.add("config.rollout_timeout_seconds", "range", "10-3600")
	}
	c.HealthCheck.validate(&fe, "config.health_check")
	if err := fe.err(); err != nil {
		return nil, err
	}
	return json.Marshal(c)
}

func (c KubernetesCredentials) validate(required bool) error {
	if c.Kubeconfig == "" {
		if required {
			return apperr.Validation([]apperr.FieldError{{Field: "credentials.kubeconfig", Rule: "required"}})
		}
		return nil
	}
	if _, err := parseKubeconfig(c.Kubeconfig); err != nil {
		return apperr.Validation([]apperr.FieldError{{Field: "credentials.kubeconfig", Rule: "kubeconfig", Param: err.Error()}})
	}
	return nil
}

// Strategies each kind supports: blue/green needs a traffic switch OpsHub controls, which
// only Kubernetes Services provide.
func supports(kind store.DeployTargetKind, strategy store.DeployStrategy) bool {
	return strategy == store.DeployStrategyRolling || kind == KindKubernetes
}
