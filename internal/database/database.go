// Package database manages the PostgreSQL connection pool and schema migrations.
package database

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5" // registers "pgx5://"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/opshub/opshub/db"
)

// Connect opens a pgx pool and verifies connectivity.
func Connect(ctx context.Context, dsn string, maxConns int32) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	cfg.MaxConns = maxConns
	cfg.MaxConnIdleTime = 5 * time.Minute
	// All timestamps are handled in UTC; display conversion happens in the client.
	cfg.ConnConfig.RuntimeParams["timezone"] = "UTC"

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}

// Migrator wraps golang-migrate with the embedded migrations.
type Migrator struct{ m *migrate.Migrate }

func NewMigrator(dsn string) (*Migrator, error) {
	src, err := iofs.New(db.Migrations, "migrations")
	if err != nil {
		return nil, fmt.Errorf("open embedded migrations: %w", err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	u.Scheme = "pgx5"
	m, err := migrate.NewWithSourceInstance("iofs", src, u.String())
	if err != nil {
		return nil, fmt.Errorf("init migrator: %w", err)
	}
	return &Migrator{m: m}, nil
}

// Up applies all pending migrations. It is a no-op when the schema is current.
func (mg *Migrator) Up() error {
	if err := mg.m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}

// Down rolls back every migration. Intended for tests and local development only.
func (mg *Migrator) Down() error {
	if err := mg.m.Down(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}

// Version returns the current schema version and dirty flag.
func (mg *Migrator) Version() (uint, bool, error) {
	v, dirty, err := mg.m.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		return 0, false, nil
	}
	return v, dirty, err
}

func (mg *Migrator) Close() error {
	srcErr, dbErr := mg.m.Close()
	return errors.Join(srcErr, dbErr)
}
