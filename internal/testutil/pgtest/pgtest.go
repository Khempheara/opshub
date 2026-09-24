// Package pgtest provides a migrated PostgreSQL 17 database for integration tests.
// One container is started lazily per test binary and reaped by testcontainers' Ryuk
// when the process exits; tests share it and isolate themselves with unique data.
package pgtest

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/opshub/opshub/internal/database"
	"github.com/opshub/opshub/internal/jobs"
)

var (
	once    sync.Once
	pool    *pgxpool.Pool
	dsn     string
	initErr error
)

// Pool returns a pool to the shared, fully migrated test database. Skipped with -short.
func Pool(t testing.TB) *pgxpool.Pool {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test: requires Docker")
	}
	once.Do(start)
	if initErr != nil {
		t.Fatalf("pgtest: %v", initErr)
	}
	return pool
}

// DSN returns the connection string of the shared test database.
func DSN(t testing.TB) string {
	Pool(t)
	return dsn
}

func start() {
	ctx := context.Background()
	ctr, err := postgres.Run(ctx, "postgres:17-alpine",
		postgres.WithDatabase("opshub"), postgres.WithUsername("opshub"), postgres.WithPassword("opshub"),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").
			WithOccurrence(2).WithStartupTimeout(90*time.Second)),
	)
	if err != nil {
		initErr = err
		return
	}
	if dsn, err = ctr.ConnectionString(ctx, "sslmode=disable"); err != nil {
		initErr = err
		return
	}
	m, err := database.NewMigrator(dsn)
	if err != nil {
		initErr = err
		return
	}
	defer func() { _ = m.Close() }()
	if err := m.Up(); err != nil {
		initErr = err
		return
	}
	if pool, err = database.Connect(ctx, dsn, 10); err != nil {
		initErr = err
		return
	}
	initErr = jobs.Migrate(ctx, pool, true)
}
