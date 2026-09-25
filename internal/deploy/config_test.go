package deploy

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/store"
)

func fieldsOf(t *testing.T, err error) []string {
	t.Helper()
	require.Error(t, err)
	ae, ok := apperr.From(err)
	require.True(t, ok, "%v", err)
	require.Equal(t, apperr.CodeValidation, ae.Code)
	var out []string
	for _, f := range ae.Details["fields"].([]apperr.FieldError) {
		out = append(out, f.Field+":"+f.Rule)
	}
	return out
}

func TestImagePattern(t *testing.T) {
	for _, ok := range []string{
		"nginx", "nginx:1.27", "library/nginx:alpine", "ghcr.io/acme/api:3f2a9c1", "registry.example.com:5000/team/app:v1.2.3",
		"app@sha256:" + strings.Repeat("a", 64), "localhost:5000/app", "my-app_x:latest",
	} {
		assert.True(t, ImagePattern.MatchString(ok), ok)
	}
	for _, bad := range []string{"", "Nginx", "app:tag with space", "app;rm -rf /", "app:$(id)", "-app", "app::1", "app:'x'"} {
		assert.False(t, ImagePattern.MatchString(bad), bad)
	}
}

func TestExpandVersion(t *testing.T) {
	vars := map[string]string{"OPSHUB_COMMIT_SHA": "3f2a9c1", "REG": "ghcr.io/acme"}
	v, missing := expandVersion("${REG}/api:${OPSHUB_COMMIT_SHA}", vars)
	assert.Equal(t, "ghcr.io/acme/api:3f2a9c1", v)
	assert.Empty(t, missing)
	_, missing = expandVersion("${DEPLOY_VERSION}", vars)
	assert.Equal(t, []string{"DEPLOY_VERSION"}, missing)
	v, _ = expandVersion("app:$OPSHUB_COMMIT_SHA", vars)
	assert.Equal(t, "app:3f2a9c1", v)
}

func TestSSHConfig(t *testing.T) {
	c := SSHConfig{Hosts: []string{"web-1.internal", "10.0.0.5:2222"}, User: "deploy", Command: "./deploy.sh",
		HostKeys: map[string]string{"web-1.internal": "SHA256:" + strings.Repeat("A", 43)}}
	raw, err := c.normalize()
	require.NoError(t, err)
	var got SSHConfig
	require.NoError(t, json.Unmarshal(raw, &got))
	assert.Equal(t, []string{"web-1.internal:22", "10.0.0.5:2222"}, got.Hosts)
	assert.Equal(t, 1, got.BatchSize)
	assert.Contains(t, got.HostKeys, "web-1.internal:22", "pins are keyed by host:port")

	bad := SSHConfig{Hosts: []string{"bad host"}, User: "Root!", Command: " ", BatchSize: 99,
		HostKeys: map[string]string{"x": "md5:nope"}, HealthCheck: &HealthCheck{URL: "ftp://x", TimeoutSeconds: 5000}}
	_, err = bad.normalize()
	assert.ElementsMatch(t, []string{
		"config.hosts[0]:host", "config.user:pattern", "config.host_keys:fingerprint", "config.command:range",
		"config.batch_size:range", "config.health_check.url:url", "config.health_check.timeout_seconds:range",
	}, fieldsOf(t, err))
	_, err = (&SSHConfig{User: "deploy", Command: "x"}).normalize()
	assert.Contains(t, fieldsOf(t, err), "config.hosts:range")
}

func TestDockerConfig(t *testing.T) {
	c := DockerConfig{Connection: DockerViaSSH, Host: "docker.internal", User: "deploy", Container: "web", Ports: []string{"80:8080"}}
	raw, err := c.normalize(false)
	require.NoError(t, err)
	var got DockerConfig
	require.NoError(t, json.Unmarshal(raw, &got))
	assert.Equal(t, "docker.internal:22", got.Host)
	assert.Equal(t, "/var/run/docker.sock", got.Socket)
	assert.Equal(t, "unless-stopped", got.Restart)
	assert.Equal(t, 1, got.Replicas)

	_, err = (&DockerConfig{Connection: DockerViaLocal, Container: "web"}).normalize(false)
	assert.Equal(t, []string{"config.connection:local_docker_disabled"}, fieldsOf(t, err))
	_, err = (&DockerConfig{Connection: DockerViaLocal, Container: "web"}).normalize(true)
	require.NoError(t, err)

	_, err = (&DockerConfig{Connection: "ftp", Container: "", Replicas: 2, Ports: []string{"80"}, Restart: "sometimes",
		Env: map[string]string{"1BAD": "x"}}).normalize(true)
	assert.ElementsMatch(t, []string{
		"config.connection:oneof", "config.container:pattern", "config.ports[0]:port", "config.ports:ports_need_one_replica",
		"config.env.1BAD:variable_name", "config.restart:oneof",
	}, fieldsOf(t, err))
}

func TestKubernetesConfig(t *testing.T) {
	c := KubernetesConfig{Deployment: "web"}
	raw, err := c.normalize()
	require.NoError(t, err)
	var got KubernetesConfig
	require.NoError(t, json.Unmarshal(raw, &got))
	assert.Equal(t, "default", got.Namespace)
	assert.Equal(t, 300, got.RolloutTimeoutSeconds)
	_, err = (&KubernetesConfig{Namespace: "Prod", Deployment: strings.Repeat("a", 60), RolloutTimeoutSeconds: 5}).normalize()
	assert.ElementsMatch(t, []string{"config.namespace:pattern", "config.deployment:pattern", "config.rollout_timeout_seconds:range"}, fieldsOf(t, err))
}

func TestSupports(t *testing.T) {
	assert.True(t, supports(KindSSH, store.DeployStrategyRolling))
	assert.False(t, supports(KindSSH, store.DeployStrategyBlueGreen))
	assert.False(t, supports(KindDocker, store.DeployStrategyBlueGreen))
	assert.True(t, supports(KindKubernetes, store.DeployStrategyBlueGreen))
}

// selfSigned returns a PEM certificate and key.
func selfSigned(t *testing.T, cn string) ([]byte, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: cn}, DNSNames: []string{cn, "localhost"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	kb, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb})
}

func kubeconfigYAML(server string, ca []byte, user string) string {
	return `apiVersion: v1
kind: Config
current-context: ci
clusters:
- name: prod
  cluster:
    server: ` + server + `
    certificate-authority-data: ` + base64.StdEncoding.EncodeToString(ca) + `
users:
- name: opshub
  user:
` + user + `
contexts:
- name: ci
  context: {cluster: prod, user: opshub, namespace: web}
`
}

func TestParseKubeconfig(t *testing.T) {
	ca, _ := selfSigned(t, "k8s")
	cert, key := selfSigned(t, "client")
	kc, err := parseKubeconfig(kubeconfigYAML("https://k8s.example.com:6443", ca, "    token: abc.def"))
	require.NoError(t, err)
	assert.Equal(t, "https://k8s.example.com:6443", kc.Server)
	assert.Equal(t, "abc.def", kc.Token)
	assert.Equal(t, "web", kc.Namespace)
	kc, err = parseKubeconfig(kubeconfigYAML("https://k8s:6443", ca, "    client-certificate-data: "+base64.StdEncoding.EncodeToString(cert)+
		"\n    client-key-data: "+base64.StdEncoding.EncodeToString(key)))
	require.NoError(t, err)
	assert.NotEmpty(t, kc.ClientCert)

	for name, c := range map[string]struct{ server, user, want string }{
		"http":     {"http://k8s", "    token: x", "https://"},
		"exec":     {"https://k8s", "    exec: {command: aws}", "exec"},
		"file":     {"https://k8s", "    tokenFile: /t", "embed"},
		"no creds": {"https://k8s", "    username: x", "no token"},
	} {
		_, err := parseKubeconfig(kubeconfigYAML(c.server, ca, c.user))
		assert.ErrorContains(t, err, c.want, name)
	}
	_, err = parseKubeconfig("not: [yaml")
	assert.Error(t, err)
	insecure := strings.Replace(kubeconfigYAML("https://k8s", ca, "    token: x"), "certificate-authority-data:", "insecure-skip-tls-verify: true\n    x-ca:", 1)
	_, err = parseKubeconfig(insecure)
	assert.ErrorContains(t, err, "insecure")
}

func TestShellQuote(t *testing.T) {
	assert.Equal(t, `'it'\''s'`, shellQuote("it's"))
	assert.Equal(t, `'$(id)'`, shellQuote("$(id)"))
}
