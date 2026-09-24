// Command api runs the OpsHub API server.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata" // distroless images ship without a zoneinfo database

	"github.com/opshub/opshub/internal/config"
	"github.com/opshub/opshub/internal/database"
	"github.com/opshub/opshub/internal/logging"
	"github.com/opshub/opshub/internal/server"
	"github.com/opshub/opshub/internal/telemetry"
)

// version is set at build time: -ldflags "-X main.version=..."
var version = "dev"

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	logger := logging.New(os.Stdout, cfg.LogFormat, cfg.LogLevel)
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	shutdownTracing, err := telemetry.SetupTracing(ctx, cfg.OTLPEndpoint, "opshub-api", version)
	if err != nil {
		return err
	}

	if cfg.MigrateOnStart {
		if err := migrate(cfg.DatabaseURL, logger); err != nil {
			return err
		}
	}

	pool, err := database.Connect(ctx, cfg.DatabaseURL, cfg.DBMaxConns)
	if err != nil {
		return err
	}
	defer pool.Close()

	srv := &http.Server{
		Addr: cfg.HTTPAddr,
		Handler: server.New(server.Deps{
			Config:  cfg,
			Logger:  logger,
			DB:      pool,
			Metrics: telemetry.NewMetrics(),
			Version: version,
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
	return errors.Join(srv.Shutdown(shutdownCtx), shutdownTracing(shutdownCtx))
}

func migrate(dsn string, logger *slog.Logger) (err error) {
	m, err := database.NewMigrator(dsn)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, m.Close()) }()
	if err := m.Up(); err != nil {
		return fmt.Errorf("migrate up: %w", err)
	}
	v, dirty, err := m.Version()
	if err != nil {
		return err
	}
	logger.Info("database schema ready", "version", v, "dirty", dirty)
	return nil
}
