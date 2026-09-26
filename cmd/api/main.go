// Command opshub-api runs the OpsHub API server and its operator subcommands:
//
//	opshub-api [serve]              run the API + background workers (default)
//	opshub-api migrate up|down|status
//	opshub-api seed                 create demo data (not in production)
//	opshub-api keys generate        print new OPSHUB_MASTER_KEYS / OPSHUB_JWT_KEYS values
//	opshub-api healthcheck          exit 0 if the local server is live (for distroless images)
//	opshub-api version
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"syscall"
	"time"
	_ "time/tzdata" // distroless images ship without a zoneinfo database

	riverpkg "github.com/riverqueue/river"

	"github.com/opshub/opshub/internal/alert"
	"github.com/opshub/opshub/internal/auditlog"
	"github.com/opshub/opshub/internal/auth"
	"github.com/opshub/opshub/internal/auth/sso"
	"github.com/opshub/opshub/internal/authn"
	"github.com/opshub/opshub/internal/blob"
	"github.com/opshub/opshub/internal/config"
	"github.com/opshub/opshub/internal/crypto"
	"github.com/opshub/opshub/internal/dashboard"
	"github.com/opshub/opshub/internal/database"
	"github.com/opshub/opshub/internal/deploy"
	"github.com/opshub/opshub/internal/events"
	"github.com/opshub/opshub/internal/gitprovider"
	"github.com/opshub/opshub/internal/i18n"
	"github.com/opshub/opshub/internal/idempotency"
	"github.com/opshub/opshub/internal/infra"
	"github.com/opshub/opshub/internal/jobs"
	"github.com/opshub/opshub/internal/keyrotate"
	"github.com/opshub/opshub/internal/logging"
	"github.com/opshub/opshub/internal/logs"
	"github.com/opshub/opshub/internal/mail"
	"github.com/opshub/opshub/internal/monitor"
	"github.com/opshub/opshub/internal/notify"
	"github.com/opshub/opshub/internal/org"
	"github.com/opshub/opshub/internal/pipeline"
	"github.com/opshub/opshub/internal/project"
	"github.com/opshub/opshub/internal/runners"
	"github.com/opshub/opshub/internal/safehttp"
	"github.com/opshub/opshub/internal/secret"
	"github.com/opshub/opshub/internal/seed"
	"github.com/opshub/opshub/internal/server"
	"github.com/opshub/opshub/internal/telemetry"
)

// version is set at build time: -ldflags "-X main.version=..."
var version = "dev"

func main() {
	cmd, args := "serve", os.Args[1:]
	if len(args) > 0 {
		cmd, args = args[0], args[1:]
	}
	var err error
	switch cmd {
	case "serve":
		err = serve()
	case "migrate":
		err = migrateCmd(args)
	case "seed":
		err = seedCmd()
	case "keys":
		err = keysCmd(args)
	case "healthcheck":
		err = healthcheck()
	case "version":
		fmt.Println(version)
	default:
		err = fmt.Errorf("unknown command %q (serve, migrate, seed, keys, healthcheck, version)", cmd)
	}
	if err != nil {
		slog.Error("fatal", "command", cmd, "error", err)
		os.Exit(1)
	}
}

func loadConfig() (config.Config, *slog.Logger, error) {
	cfg, err := config.Load()
	if err != nil {
		return cfg, nil, fmt.Errorf("config: %w", err)
	}
	logger := logging.New(os.Stdout, cfg.LogFormat, cfg.LogLevel)
	slog.SetDefault(logger)
	return cfg, logger, nil
}

func serve() error {
	cfg, logger, err := loadConfig()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	shutdownTracing, err := telemetry.SetupTracing(ctx, cfg.OTLPEndpoint, "opshub-api", version)
	if err != nil {
		return err
	}
	if cfg.MigrateOnStart {
		if err := migrateUp(ctx, cfg, logger); err != nil {
			return err
		}
	}
	pool, err := database.Connect(ctx, cfg.DatabaseURL, cfg.DBMaxConns)
	if err != nil {
		return err
	}
	defer pool.Close()

	masterKeys, _ := config.ParseKeyRing(cfg.MasterKeys) // validated by config.Load
	jwtKeys, _ := config.ParseKeyRing(cfg.JWTKeys)
	keyRing, err := crypto.NewKeyRing(masterKeys)
	if err != nil {
		return err
	}
	signer, err := authn.NewJWTSigner(jwtKeys, nil)
	if err != nil {
		return err
	}
	bundle, err := i18n.NewBundle()
	if err != nil {
		return err
	}
	// Services that enqueue jobs get a deferred inserter; the River client is created once
	// their workers can be registered.
	inserter := &jobs.Deferred{}
	outboundCIDRs, _ := safehttp.ParseCIDRs(cfg.OutboundAllowedCIDRs) // validated by config.Load
	gitFactory := gitprovider.Factory{HTTP: safehttp.NewClient(safehttp.Options{AllowedCIDRs: outboundCIDRs})}
	projectSvc := project.NewService(pool, keyRing, gitFactory, inserter, project.Config{PublicURL: cfg.PublicURL}, logger)
	pipelineSvc := pipeline.NewService(pool, projectSvc, logger)
	blobs, err := blob.NewLocal(cfg.BlobDir)
	if err != nil {
		return err
	}
	runnerSvc := runners.NewService(pool, pipelineSvc, projectSvc, blobs, runners.Config{
		ArtifactMaxBytes: cfg.ArtifactMaxBytes, CacheMaxBytes: cfg.CacheMaxBytes,
		CacheQuotaBytes: cfg.CacheQuotaBytes, SourceMaxBytes: cfg.SourceMaxBytes,
	}, logger)

	deploySvc := deploy.NewService(pool, keyRing, pipelineSvc, inserter, deploy.Config{
		OutboundAllowedCIDRs: outboundCIDRs, AllowLocalDocker: cfg.DeployLocalDocker,
	}, logger)

	infraSvc := infra.NewService(pool, infra.Config{OutboundAllowedCIDRs: outboundCIDRs}, logger)

	secretSvc := secret.NewService(pool, keyRing, logger)
	runnerSvc.SetSecrets(secretSvc)

	monitorSvc := monitor.NewService(pool, monitor.Config{OutboundAllowedCIDRs: outboundCIDRs}, logger)
	mailer := &mail.SMTPSender{Config: cfg.SMTP}
	notifySvc := notify.NewService(pool, keyRing, bundle, mailer, notify.Config{
		PublicURL: cfg.PublicURL, DefaultLocale: cfg.DefaultLocale, OutboundAllowedCIDRs: outboundCIDRs,
	}, logger)
	alertSvc := alert.NewService(pool, inserter, logger)
	auditSvc := auditlog.NewService(pool, bundle, logger)
	logSvc := logs.NewService(pool, logs.Config{RetentionDays: cfg.LogRetentionDays}, logger)
	metrics := telemetry.NewMetrics()
	metrics.RegisterPlatform(pool, logger)
	logSvc.SetIngestObserver(metrics.ObserveLogLines)

	river, err := jobs.NewClient(jobs.Deps{
		Pool: pool, Logger: logger,
		Renderer: &mail.Renderer{Bundle: bundle},
		Sender:   mailer,
		Register: func(w *riverpkg.Workers) {
			pipelineSvc.Register(w)
			runnerSvc.Register(w)
			deploySvc.Register(w)
			infraSvc.Register(w)
			monitorSvc.Register(w)
			notifySvc.Register(w)
			alertSvc.Register(w)
			logSvc.Register(w)
		},
		Periodic: slices.Concat(pipeline.Periodic(), runners.Periodic(), infra.Periodic(), monitor.Periodic(), alert.Periodic(), logs.Periodic()),
	})
	if err != nil {
		return err
	}
	inserter.Bind(river)
	if err := river.Start(ctx); err != nil {
		return fmt.Errorf("start workers: %w", err)
	}
	hub := events.NewHub(pool, logger)
	go hub.Run(ctx)

	ssoRegistry := sso.NewRegistry(cfg.SSO, cfg.PublicURL, nil)
	authSvc := auth.NewService(pool, auth.Config{
		PublicURL: cfg.PublicURL, AllowSignup: cfg.AllowSignup, BootstrapAdminEmail: cfg.BootstrapAdminEmail,
		DefaultLocale: cfg.DefaultLocale, DefaultTimezone: cfg.DefaultTimezone,
	}, authn.NewHasher(authn.DefaultArgon2Params), signer, keyRing, river, logger)

	srv := &http.Server{
		Addr: cfg.HTTPAddr,
		Handler: server.New(server.Deps{
			Config:        cfg,
			Logger:        logger,
			DB:            pool,
			Metrics:       metrics,
			Version:       version,
			Authenticator: &authn.Authenticator{JWT: signer, Tokens: authSvc},
			SSOProviders:  ssoRegistry.Names(),
			Modules: []server.Module{
				auth.NewHandler(authSvc, ssoRegistry, keyRing, auth.HandlerConfig{
					PublicURL: cfg.PublicURL, SecureCookies: cfg.SecureCookies(),
					AllowedOrigins: append([]string{originOf(cfg.PublicURL)}, cfg.CORSAllowedOrigins...),
					LoginPerMinute: cfg.AuthLoginPerMinute, EmailPerMinute: cfg.AuthEmailPerMinute,
				}, logger),
				org.NewHandler(org.NewService(pool, river, org.Config{PublicURL: cfg.PublicURL})),
				project.NewHandler(projectSvc, idempotency.Middleware(pool)),
				pipeline.NewHandler(pipelineSvc, hub, idempotency.Middleware(pool)),
				runners.NewHandler(runnerSvc),
				deploy.NewHandler(deploySvc, hub, idempotency.Middleware(pool)),
				infra.NewHandler(infraSvc),
				secret.NewHandler(secretSvc),
				monitor.NewHandler(monitorSvc),
				alert.NewHandler(alertSvc),
				notify.NewHandler(notifySvc),
				logs.NewHandler(logSvc),
				auditlog.NewHandler(auditSvc),
				dashboard.NewHandler(dashboard.NewService(pool)),
			},
		}),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		// No WriteTimeout: SSE log streams are long-lived. Handlers bound their own work.
		IdleTimeout: 120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("api listening", "addr", cfg.HTTPAddr, "version", version, "env", cfg.Env)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("http server: %w", err)
		}
	case <-ctx.Done():
		logger.Info("shutting down", "timeout", cfg.ShutdownTimeout)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	return errors.Join(srv.Shutdown(shutdownCtx), river.Stop(shutdownCtx), shutdownTracing(shutdownCtx))
}

func originOf(u string) string {
	p, err := url.Parse(u)
	if err != nil {
		return u
	}
	return p.Scheme + "://" + p.Host
}

// migrateUp applies OpsHub migrations, then River's, using the schema-owner connection.
func migrateUp(ctx context.Context, cfg config.Config, logger *slog.Logger) (err error) {
	m, err := database.NewMigrator(cfg.MigrateDatabaseURL)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, m.Close()) }()
	if err := m.Up(); err != nil {
		return fmt.Errorf("migrate up: %w", err)
	}
	pool, err := database.Connect(ctx, cfg.MigrateDatabaseURL, 2)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := jobs.Migrate(ctx, pool, true); err != nil {
		return err
	}
	v, dirty, err := m.Version()
	if err != nil {
		return err
	}
	logger.Info("database schema ready", "version", v, "dirty", dirty)
	return nil
}

func migrateCmd(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: opshub-api migrate up|down|status")
	}
	cfg, logger, err := loadConfig()
	if err != nil {
		return err
	}
	ctx := context.Background()
	switch args[0] {
	case "up":
		return migrateUp(ctx, cfg, logger)
	case "down":
		if cfg.IsProduction() {
			return errors.New("refusing to roll back all migrations in production")
		}
		pool, err := database.Connect(ctx, cfg.MigrateDatabaseURL, 2)
		if err != nil {
			return err
		}
		defer pool.Close()
		if err := jobs.Migrate(ctx, pool, false); err != nil {
			return err
		}
		m, err := database.NewMigrator(cfg.MigrateDatabaseURL)
		if err != nil {
			return err
		}
		defer func() { _ = m.Close() }()
		if err := m.Down(); err != nil {
			return err
		}
		logger.Info("all migrations rolled back")
		return nil
	case "status":
		m, err := database.NewMigrator(cfg.MigrateDatabaseURL)
		if err != nil {
			return err
		}
		defer func() { _ = m.Close() }()
		v, dirty, err := m.Version()
		if err != nil {
			return err
		}
		fmt.Printf("schema version %d (dirty=%t)\n", v, dirty)
		return nil
	default:
		return fmt.Errorf("unknown migrate action %q", args[0])
	}
}

// keysRotate re-encrypts all stored values with the first (active) master key. Safe while
// the API runs; rerun it until nothing fails, then remove the old key.
func keysRotate() error {
	cfg, _, err := loadConfig()
	if err != nil {
		return err
	}
	ctx := context.Background()
	pool, err := database.Connect(ctx, cfg.DatabaseURL, 2)
	if err != nil {
		return err
	}
	defer pool.Close()
	masterKeys, _ := config.ParseKeyRing(cfg.MasterKeys) // validated by config.Load
	keys, err := crypto.NewKeyRing(masterKeys)
	if err != nil {
		return err
	}
	fmt.Printf("Re-encrypting with the active master key %q\n", keys.ActiveKeyID())
	results, err := keyrotate.Rotate(ctx, pool, keys)
	for _, r := range results {
		fmt.Printf("  %-34s %6d scanned  %6d re-encrypted  %4d failed\n", r.Column, r.Scanned, r.Rewrapped, r.Failed)
	}
	if errors.Is(err, keyrotate.ErrUnreadable) {
		return fmt.Errorf("%w: keep the old keys configured and check the failed columns", err)
	}
	if err != nil {
		return err
	}
	if len(masterKeys) > 1 {
		fmt.Println("Done. Older keys can now be removed from OPSHUB_MASTER_KEYS.")
	} else {
		fmt.Println("Done.")
	}
	return nil
}

func seedCmd() error {
	cfg, _, err := loadConfig()
	if err != nil {
		return err
	}
	if cfg.IsProduction() {
		return errors.New("refusing to seed demo data in production")
	}
	ctx := context.Background()
	if err := migrateUp(ctx, cfg, slog.Default()); err != nil {
		return err
	}
	pool, err := database.Connect(ctx, cfg.DatabaseURL, 2)
	if err != nil {
		return err
	}
	defer pool.Close()
	masterKeys, _ := config.ParseKeyRing(cfg.MasterKeys) // validated by config.Load
	keys, err := crypto.NewKeyRing(masterKeys)
	if err != nil {
		return err
	}
	return seed.Run(ctx, pool, seed.Options{
		Password:         os.Getenv("OPSHUB_SEED_PASSWORD"),
		Hasher:           authn.NewHasher(authn.DefaultArgon2Params),
		Out:              os.Stdout,
		Keys:             keys,
		LogRetentionDays: cfg.LogRetentionDays,
	})
}

// healthcheck probes /healthz on the local listener. Distroless images have no curl.
// Only the port is taken from OPSHUB_HTTP_ADDR; the probe always targets 127.0.0.1.
func healthcheck() error {
	addr := os.Getenv("OPSHUB_HTTP_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("invalid port in OPSHUB_HTTP_ADDR %q", addr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	target := fmt.Sprintf("http://127.0.0.1:%d/healthz", port)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil) // #nosec G704 -- loopback only; port is a validated integer
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req) // #nosec G704 -- loopback only; port is a validated integer
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthz returned %d", resp.StatusCode)
	}
	return nil
}

func keysCmd(args []string) error {
	if len(args) == 1 && args[0] == "rotate" {
		return keysRotate()
	}
	if len(args) != 1 || args[0] != "generate" {
		return errors.New("usage: opshub-api keys generate | keys rotate")
	}
	id := time.Now().UTC().Format("20060102")
	gen := func() string {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			panic(err)
		}
		return base64.StdEncoding.EncodeToString(b)
	}
	fmt.Printf("OPSHUB_MASTER_KEYS=m%s:%s\n", id, gen())
	fmt.Printf("OPSHUB_JWT_KEYS=k%s:%s\n", id, gen())
	return nil
}
