package deploy

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// kubeconfig is the subset of a kubeconfig OpsHub supports: one context whose cluster has
// embedded CA data and whose user has a bearer token or an embedded client certificate.
// Exec and auth-provider plugins (EKS/GKE/AKS defaults) are refused: create a service
// account token for OpsHub instead.
type kubeconfig struct {
	Server     string
	CA         []byte
	ServerName string
	Token      string
	ClientCert []byte
	ClientKey  []byte
	Namespace  string
}

func parseKubeconfig(text string) (kubeconfig, error) {
	var raw struct {
		CurrentContext string `yaml:"current-context"`
		Clusters       []struct {
			Name    string `yaml:"name"`
			Cluster struct {
				Server        string `yaml:"server"`
				CAData        string `yaml:"certificate-authority-data"`
				CAFile        string `yaml:"certificate-authority"`
				Insecure      bool   `yaml:"insecure-skip-tls-verify"`
				TLSServerName string `yaml:"tls-server-name"`
			} `yaml:"cluster"`
		} `yaml:"clusters"`
		Users []struct {
			Name string `yaml:"name"`
			User struct {
				Token          string         `yaml:"token"`
				TokenFile      string         `yaml:"tokenFile"`
				ClientCertData string         `yaml:"client-certificate-data"`
				ClientKeyData  string         `yaml:"client-key-data"`
				ClientCertFile string         `yaml:"client-certificate"`
				Exec           map[string]any `yaml:"exec"`
				AuthProvider   map[string]any `yaml:"auth-provider"`
			} `yaml:"user"`
		} `yaml:"users"`
		Contexts []struct {
			Name    string `yaml:"name"`
			Context struct {
				Cluster   string `yaml:"cluster"`
				User      string `yaml:"user"`
				Namespace string `yaml:"namespace"`
			} `yaml:"context"`
		} `yaml:"contexts"`
	}
	if err := yaml.Unmarshal([]byte(text), &raw); err != nil {
		return kubeconfig{}, errors.New("not valid YAML")
	}
	ctxName := raw.CurrentContext
	if ctxName == "" && len(raw.Contexts) == 1 {
		ctxName = raw.Contexts[0].Name
	}
	var out kubeconfig
	var clusterName, userName string
	for _, c := range raw.Contexts {
		if c.Name == ctxName {
			clusterName, userName, out.Namespace = c.Context.Cluster, c.Context.User, c.Context.Namespace
		}
	}
	if clusterName == "" {
		return out, errors.New("no current context")
	}
	found := false
	for _, c := range raw.Clusters {
		if c.Name != clusterName {
			continue
		}
		found = true
		u, err := url.Parse(c.Cluster.Server)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return out, errors.New("the cluster server must be an https:// URL")
		}
		if c.Cluster.Insecure {
			return out, errors.New("insecure-skip-tls-verify isn't allowed; include certificate-authority-data")
		}
		if c.Cluster.CAFile != "" {
			return out, errors.New("embed the CA as certificate-authority-data (files can't be read)")
		}
		out.Server = strings.TrimRight(c.Cluster.Server, "/")
		out.ServerName = c.Cluster.TLSServerName
		if c.Cluster.CAData != "" {
			ca, err := base64.StdEncoding.DecodeString(c.Cluster.CAData)
			if err != nil || !x509.NewCertPool().AppendCertsFromPEM(ca) {
				return out, errors.New("certificate-authority-data isn't a PEM certificate")
			}
			out.CA = ca
		}
	}
	if !found {
		return out, fmt.Errorf("cluster %q not found", clusterName)
	}
	found = false
	for _, u := range raw.Users {
		if u.Name != userName {
			continue
		}
		found = true
		switch {
		case u.User.Exec != nil || u.User.AuthProvider != nil:
			return out, errors.New("exec and auth-provider plugins aren't supported; use a service account token")
		case u.User.TokenFile != "" || u.User.ClientCertFile != "":
			return out, errors.New("embed credentials (token or client-certificate-data); files can't be read")
		case u.User.Token != "":
			out.Token = u.User.Token
		case u.User.ClientCertData != "":
			cert, err1 := base64.StdEncoding.DecodeString(u.User.ClientCertData)
			key, err2 := base64.StdEncoding.DecodeString(u.User.ClientKeyData)
			if err1 != nil || err2 != nil {
				return out, errors.New("client certificate data isn't base64")
			}
			if _, err := tls.X509KeyPair(cert, key); err != nil {
				return out, errors.New("client certificate and key don't match")
			}
			out.ClientCert, out.ClientKey = cert, key
		default:
			return out, errors.New("the user has no token or client certificate")
		}
	}
	if !found {
		return out, fmt.Errorf("user %q not found", userName)
	}
	return out, nil
}

// kubeClient calls the Kubernetes API.
type kubeClient struct {
	base  string
	token string
	http  *http.Client
}

var errKubeNotFound = errors.New("not found in the cluster")

func newKubeClient(kc kubeconfig, dialer *net.Dialer) (*kubeClient, error) {
	tlsConf := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: kc.ServerName}
	if len(kc.CA) > 0 {
		pool := x509.NewCertPool()
		pool.AppendCertsFromPEM(kc.CA)
		tlsConf.RootCAs = pool
	}
	if len(kc.ClientCert) > 0 {
		pair, err := tls.X509KeyPair(kc.ClientCert, kc.ClientKey)
		if err != nil {
			return nil, err
		}
		tlsConf.Certificates = []tls.Certificate{pair}
	}
	return &kubeClient{
		base:  kc.Server,
		token: kc.Token,
		http: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				Proxy: nil, DialContext: dialer.DialContext, TLSClientConfig: tlsConf,
				TLSHandshakeTimeout: 10 * time.Second, MaxIdleConns: 4, IdleConnTimeout: 30 * time.Second,
			},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

func (k *kubeClient) call(ctx context.Context, method, path, contentType string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, k.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if k.token != "" {
		req.Header.Set("Authorization", "Bearer "+k.token)
	}
	resp, err := k.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return errKubeNotFound
	}
	if resp.StatusCode >= 300 {
		var st struct {
			Message string `json:"message"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&st)
		return fmt.Errorf("kubernetes %s %s: %s: %s", method, path, resp.Status, st.Message)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out)
}

const strategicMerge = "application/strategic-merge-patch+json"

func deploymentPath(ns, name string) string {
	return "/apis/apps/v1/namespaces/" + url.PathEscape(ns) + "/deployments/" + url.PathEscape(name)
}

func servicePath(ns, name string) string {
	return "/api/v1/namespaces/" + url.PathEscape(ns) + "/services/" + url.PathEscape(name)
}

// k8sDeployment is the part of a Deployment the deployer reads.
type k8sDeployment struct {
	Metadata struct {
		Name       string `json:"name"`
		Generation int64  `json:"generation"`
	} `json:"metadata"`
	Spec struct {
		Replicas *int32 `json:"replicas"`
		Template struct {
			Spec struct {
				Containers []struct {
					Name  string `json:"name"`
					Image string `json:"image"`
				} `json:"containers"`
			} `json:"spec"`
		} `json:"template"`
	} `json:"spec"`
	Status struct {
		ObservedGeneration  int64 `json:"observedGeneration"`
		Replicas            int32 `json:"replicas"`
		UpdatedReplicas     int32 `json:"updatedReplicas"`
		AvailableReplicas   int32 `json:"availableReplicas"`
		UnavailableReplicas int32 `json:"unavailableReplicas"`
		Conditions          []struct {
			Type    string `json:"type"`
			Status  string `json:"status"`
			Reason  string `json:"reason"`
			Message string `json:"message"`
		} `json:"conditions"`
	} `json:"status"`
}

func (d k8sDeployment) replicas() int32 {
	if d.Spec.Replicas == nil {
		return 1
	}
	return *d.Spec.Replicas
}

// container returns the named container (or the first) and its image.
func (d k8sDeployment) container(name string) (string, string, error) {
	cs := d.Spec.Template.Spec.Containers
	for _, c := range cs {
		if name == "" || c.Name == name {
			return c.Name, c.Image, nil
		}
	}
	return "", "", fmt.Errorf("deployment %s has no container %q", d.Metadata.Name, name)
}

func (k *kubeClient) getDeployment(ctx context.Context, ns, name string) (k8sDeployment, error) {
	var d k8sDeployment
	err := k.call(ctx, http.MethodGet, deploymentPath(ns, name), "", nil, &d)
	return d, err
}

// setImage patches a container's image. The annotation forces a rollout even when the
// image doesn't change (a redeploy or a rollback to the running version).
func (k *kubeClient) setImage(ctx context.Context, ns, name, container, image, deploymentID string, replicas *int32) error {
	spec := map[string]any{
		"template": map[string]any{
			"metadata": map[string]any{"annotations": map[string]string{"opshub.io/deployment": deploymentID}},
			"spec":     map[string]any{"containers": []map[string]string{{"name": container, "image": image}}},
		},
	}
	if replicas != nil {
		spec["replicas"] = *replicas
	}
	return k.call(ctx, http.MethodPatch, deploymentPath(ns, name), strategicMerge, map[string]any{"spec": spec}, nil)
}

// waitRollout polls until every replica runs the current template, the rollout's progress
// deadline passes, or timeout.
func (k *kubeClient) waitRollout(ctx context.Context, ns, name string, timeout time.Duration, log io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	last := ""
	for {
		d, err := k.getDeployment(ctx, ns, name)
		if err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("rollout of %s didn't finish within %s", name, timeout)
			}
			return err
		}
		want := d.replicas()
		st := d.Status
		progress := fmt.Sprintf("%s: %d of %d updated, %d available", name, st.UpdatedReplicas, want, st.AvailableReplicas)
		if progress != last {
			_, _ = fmt.Fprintln(log, progress)
			last = progress
		}
		for _, c := range st.Conditions {
			if c.Type == "Progressing" && c.Reason == "ProgressDeadlineExceeded" {
				return fmt.Errorf("rollout of %s stalled: %s", name, c.Message)
			}
		}
		if st.ObservedGeneration >= d.Metadata.Generation && st.UpdatedReplicas == want &&
			st.AvailableReplicas == want && st.Replicas == want && st.UnavailableReplicas == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("rollout of %s didn't finish within %s", name, timeout)
		case <-time.After(2 * time.Second):
		}
	}
}

// Color label that blue/green adds to pod templates and the Service selector.
const colorLabel = "opshub.io/color"

type k8sService struct {
	Spec struct {
		Selector map[string]string `json:"selector"`
	} `json:"spec"`
}

func (k *kubeClient) getService(ctx context.Context, ns, name string) (k8sService, error) {
	var s k8sService
	err := k.call(ctx, http.MethodGet, servicePath(ns, name), "", nil, &s)
	return s, err
}

// selectColor points the Service at a color ("" removes the color from the selector).
func (k *kubeClient) selectColor(ctx context.Context, ns, name, color string) error {
	var v any = color
	if color == "" {
		v = nil // strategic merge: delete the key
	}
	return k.call(ctx, http.MethodPatch, servicePath(ns, name), strategicMerge,
		map[string]any{"spec": map[string]any{"selector": map[string]any{colorLabel: v}}}, nil)
}

// cloneForColor creates <base>-<color> from a source Deployment: same pod template with the
// color label added to its selector and pods.
func (k *kubeClient) cloneForColor(ctx context.Context, ns, source, name, color, container, image, deploymentID string, replicas int32) error {
	var raw map[string]any
	if err := k.call(ctx, http.MethodGet, deploymentPath(ns, source), "", nil, &raw); err != nil {
		return err
	}
	spec, _ := raw["spec"].(map[string]any)
	if spec == nil {
		return fmt.Errorf("deployment %s has no spec", source)
	}
	meta, _ := raw["metadata"].(map[string]any)
	labels := map[string]any{}
	if l, ok := meta["labels"].(map[string]any); ok {
		for k, v := range l {
			labels[k] = v
		}
	}
	labels[colorLabel] = color
	addLabel := func(m map[string]any, key string) {
		inner, _ := m[key].(map[string]any)
		if inner == nil {
			inner = map[string]any{}
		}
		inner[colorLabel] = color
		m[key] = inner
	}
	selector, _ := spec["selector"].(map[string]any)
	if selector == nil {
		selector = map[string]any{}
	}
	addLabel(selector, "matchLabels")
	spec["selector"] = selector
	tmpl, _ := spec["template"].(map[string]any)
	tmeta, _ := tmpl["metadata"].(map[string]any)
	if tmeta == nil {
		tmeta = map[string]any{}
	}
	addLabel(tmeta, "labels")
	ann, _ := tmeta["annotations"].(map[string]any)
	if ann == nil {
		ann = map[string]any{}
	}
	ann["opshub.io/deployment"] = deploymentID
	tmeta["annotations"] = ann
	tmpl["metadata"] = tmeta
	if pod, ok := tmpl["spec"].(map[string]any); ok {
		if cs, ok := pod["containers"].([]any); ok {
			for _, c := range cs {
				if cm, ok := c.(map[string]any); ok && cm["name"] == container {
					cm["image"] = image
				}
			}
		}
	}
	spec["replicas"] = replicas
	body := map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": name, "namespace": ns, "labels": labels},
		"spec":     spec,
	}
	return k.call(ctx, http.MethodPost, "/apis/apps/v1/namespaces/"+url.PathEscape(ns)+"/deployments", "application/json", body, nil)
}

// serverVersion returns the cluster's version string.
func (k *kubeClient) serverVersion(ctx context.Context) (string, error) {
	var v struct {
		GitVersion string `json:"gitVersion"`
	}
	err := k.call(ctx, http.MethodGet, "/version", "", nil, &v)
	return v.GitVersion, err
}
