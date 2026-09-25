// Package runner is the opshub-runner agent: it registers with OpsHub, long-polls for jobs
// and runs each job's steps in Docker containers. It talks to OpsHub only over the HTTP
// runner API and imports no server packages.
package runner

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config is stored as JSON (mode 0600: it holds the runner token).
type Config struct {
	URL            string   `json:"url"`
	RunnerID       string   `json:"runner_id"`
	Token          string   `json:"token"`
	Name           string   `json:"name"`
	Labels         []string `json:"labels"`
	MaxConcurrency int      `json:"max_concurrency"`
	DockerSocket   string   `json:"docker_socket"`
	DefaultImage   string   `json:"default_image"`
	AlwaysPull     bool     `json:"always_pull"`
	CPUs           float64  `json:"cpus"`       // per step container; 0 = no limit
	MemoryMB       int64    `json:"memory_mb"`  // per step container; 0 = no limit
	PidsLimit      int64    `json:"pids_limit"` // per step container
	Network        string   `json:"network"`    // Docker network for steps; "" = default bridge
	TempDir        string   `json:"temp_dir"`   // staging for artifact and cache archives
}

// Defaults fills unset fields.
func (c *Config) Defaults() {
	if c.MaxConcurrency <= 0 {
		c.MaxConcurrency = 1
	}
	if c.DockerSocket == "" {
		c.DockerSocket = "/var/run/docker.sock"
	}
	if c.DefaultImage == "" {
		c.DefaultImage = "alpine:3.20"
	}
	if c.PidsLimit == 0 {
		c.PidsLimit = 4096
	}
	if c.TempDir == "" {
		c.TempDir = os.TempDir()
	}
}

// Validate checks a loaded config.
func (c *Config) Validate() error {
	switch {
	case !strings.HasPrefix(c.URL, "https://") && !strings.HasPrefix(c.URL, "http://"):
		return errors.New("config: url must be an http(s) URL")
	case !strings.HasPrefix(c.Token, "ohr_") || strings.HasPrefix(c.Token, "ohr_reg_"):
		return errors.New("config: token is missing or not a runner token (run `opshub-runner register`)")
	case c.MaxConcurrency > 64:
		return errors.New("config: max_concurrency must be 1-64")
	case c.CPUs < 0 || c.MemoryMB < 0:
		return errors.New("config: cpus and memory_mb can't be negative")
	}
	return nil
}

// LoadConfig reads a config file. It refuses files other users can read.
func LoadConfig(path string) (Config, error) {
	var c Config
	info, err := os.Stat(path)
	if err != nil {
		return c, err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return c, fmt.Errorf("%s is readable by other users; run: chmod 600 %s", path, path)
	}
	b, err := os.ReadFile(path) // #nosec G304 -- operator-chosen path
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	c.Defaults()
	return c, c.Validate()
}

// SaveConfig writes the config atomically with mode 0600.
func SaveConfig(path string, c Config) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".opshub-runner-*.json")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
