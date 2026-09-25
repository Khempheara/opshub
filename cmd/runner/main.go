// Command opshub-runner executes OpsHub pipeline jobs in Docker containers.
//
//	opshub-runner register --url https://opshub.example.com --token ohr_reg_… [--name n] [--labels a,b]
//	opshub-runner run [--config path]
//	opshub-runner version
//
// `run` can also register itself on first start from OPSHUB_URL and
// OPSHUB_REGISTRATION_TOKEN (used by the Docker Compose "runner" profile).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/opshub/opshub/internal/dockerapi"
	"github.com/opshub/opshub/internal/runner"
)

var version = "dev" // set with -ldflags "-X main.version=…"

func defaultConfigPath() string {
	if p := os.Getenv("OPSHUB_RUNNER_CONFIG"); p != "" {
		return p
	}
	return "/var/lib/opshub-runner/config.json"
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "register":
		err = register(os.Args[2:])
	case "run":
		err = run(os.Args[2:], logger)
	case "version":
		fmt.Println(version)
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "opshub-runner:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: opshub-runner register|run|version [flags]")
}

func splitLabels(s string) []string {
	var out []string
	for _, l := range strings.Split(s, ",") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func register(args []string) error {
	fl := flag.NewFlagSet("register", flag.ExitOnError)
	cfgPath := fl.String("config", defaultConfigPath(), "config file to write")
	url := fl.String("url", os.Getenv("OPSHUB_URL"), "OpsHub URL")
	token := fl.String("token", os.Getenv("OPSHUB_REGISTRATION_TOKEN"), "registration token (ohr_reg_…)")
	host, _ := os.Hostname()
	name := fl.String("name", envOr("OPSHUB_RUNNER_NAME", host), "runner name")
	labels := fl.String("labels", envOr("OPSHUB_RUNNER_LABELS", "linux,docker"), "comma-separated labels")
	concurrency := fl.Int("max-concurrency", 1, "jobs to run at once (1-64)")
	_ = fl.Parse(args)
	cfg := runner.Config{URL: *url, Name: *name, Labels: splitLabels(*labels), MaxConcurrency: *concurrency}
	if err := doRegister(&cfg, *token); err != nil {
		return err
	}
	if err := runner.SaveConfig(*cfgPath, cfg); err != nil {
		return err
	}
	fmt.Printf("Registered runner %q. Config saved to %s.\n", cfg.Name, *cfgPath)
	return nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func doRegister(cfg *runner.Config, token string) error {
	if cfg.URL == "" || token == "" {
		return errors.New("--url and --token are required")
	}
	cfg.Defaults()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	reg, err := runner.NewClient(cfg.URL, version).Register(ctx, runner.RegisterInput{
		Token: token, Name: cfg.Name, Labels: cfg.Labels, Version: version,
		OS: runtime.GOOS, Arch: runtime.GOARCH, MaxConcurrency: cfg.MaxConcurrency,
	})
	if err != nil {
		return fmt.Errorf("register: %w", err)
	}
	cfg.RunnerID, cfg.Token, cfg.Name, cfg.Labels = reg.ID, reg.Token, reg.Name, reg.Labels
	return nil
}

func run(args []string, logger *slog.Logger) error {
	fl := flag.NewFlagSet("run", flag.ExitOnError)
	cfgPath := fl.String("config", defaultConfigPath(), "config file")
	grace := fl.Duration("shutdown-grace", 10*time.Minute, "how long running jobs may finish after SIGTERM")
	_ = fl.Parse(args)

	cfg, err := runner.LoadConfig(*cfgPath)
	if errors.Is(err, fs.ErrNotExist) && os.Getenv("OPSHUB_REGISTRATION_TOKEN") != "" {
		host, _ := os.Hostname()
		cfg = runner.Config{
			URL: os.Getenv("OPSHUB_URL"), Name: envOr("OPSHUB_RUNNER_NAME", host),
			Labels: splitLabels(envOr("OPSHUB_RUNNER_LABELS", "linux,docker")), MaxConcurrency: 1,
		}
		if err := doRegister(&cfg, os.Getenv("OPSHUB_REGISTRATION_TOKEN")); err != nil {
			return err
		}
		if err := runner.SaveConfig(*cfgPath, cfg); err != nil {
			return err
		}
		logger.Info("registered", "name", cfg.Name, "config", *cfgPath)
	} else if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	docker, err := dockerapi.NewUnix(ctx, cfg.DockerSocket)
	if err != nil {
		return err
	}
	client := runner.NewClient(cfg.URL, version)
	exec := runner.NewExecutor(docker, client, runner.ExecConfig{
		RunnerID: cfg.RunnerID, DefaultImage: cfg.DefaultImage, AlwaysPull: cfg.AlwaysPull,
		NanoCPUs: int64(cfg.CPUs * 1e9), MemoryBytes: cfg.MemoryMB << 20, PidsLimit: cfg.PidsLimit,
		Network: cfg.Network, TempDir: cfg.TempDir,
	}, logger)
	return runner.NewAgent(cfg, version, client, exec, logger).Run(ctx, *grace)
}
