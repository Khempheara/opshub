package database_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/opshub/opshub/internal/database"
	"github.com/opshub/opshub/internal/store"
)

// startPostgres runs a throwaway PostgreSQL 17 container. Skipped with -short.
func startPostgres(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test: requires Docker")
	}
	ctx := context.Background()
	ctr, err := postgres.Run(ctx, "postgres:17-alpine",
		postgres.WithDatabase("opshub"), postgres.WithUsername("opshub"), postgres.WithPassword("opshub"),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").
			WithOccurrence(2).WithStartupTimeout(60*time.Second)),
	)
	testcontainers.CleanupContainer(t, ctr)
	require.NoError(t, err)
	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	return dsn
}

func TestMigrationsAndSchemaInvariants(t *testing.T) {
	dsn := startPostgres(t)
	ctx := context.Background()

	// Mirror production: a DML-only application role with default privileges on new tables.
	setup, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	_, err = setup.Exec(ctx, `CREATE ROLE opshub_app NOLOGIN;
		ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO opshub_app`)
	require.NoError(t, err)
	setup.Close()

	m, err := database.NewMigrator(dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = m.Close() })
	require.NoError(t, m.Up())
	require.NoError(t, m.Up(), "Up must be idempotent")

	pool, err := database.Connect(ctx, dsn, 4)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	q := store.New(pool)

	t.Run("uuid v7 defaults are time-ordered", func(t *testing.T) {
		var ids []uuid.UUID
		for range 50 {
			var id uuid.UUID
			require.NoError(t, pool.QueryRow(ctx, "SELECT uuid_generate_v7()").Scan(&id))
			assert.Equal(t, uuid.Version(7), id.Version())
			assert.Equal(t, uuid.RFC4122, id.Variant())
			ids = append(ids, id)
		}
		first, last := ids[0], ids[len(ids)-1]
		ts := func(u uuid.UUID) int64 { s, n := u.Time().UnixTime(); return s*1e3 + n/1e6 }
		assert.LessOrEqual(t, ts(first), ts(last))
		assert.InDelta(t, time.Now().UnixMilli(), ts(last), 60_000, "timestamp should be ~now")
	})

	t.Run("users: defaults, citext email uniqueness, updated_at trigger", func(t *testing.T) {
		u, err := q.CreateUser(ctx, store.CreateUserParams{
			Email: "Dara@Example.com", DisplayName: "Dara", Locale: "km", Timezone: "Asia/Phnom_Penh",
		})
		require.NoError(t, err)
		assert.Equal(t, uuid.Version(7), u.ID.Version())
		assert.False(t, u.KhmerNumerals)

		got, err := q.GetUserByEmail(ctx, "dara@example.com")
		require.NoError(t, err)
		assert.Equal(t, u.ID, got.ID)

		_, err = q.CreateUser(ctx, store.CreateUserParams{
			Email: "DARA@example.com", DisplayName: "Dup", Locale: "en", Timezone: "UTC",
		})
		assert.ErrorContains(t, err, "users_email_key")

		_, err = q.CreateUser(ctx, store.CreateUserParams{
			Email: "x@example.com", DisplayName: "X", Locale: "fr", Timezone: "UTC",
		})
		assert.ErrorContains(t, err, "users_locale_check")

		updated, err := q.UpdateUserPreferences(ctx, store.UpdateUserPreferencesParams{
			ID: u.ID, Locale: "en", Timezone: "UTC", KhmerNumerals: true,
		})
		require.NoError(t, err)
		assert.True(t, updated.UpdatedAt.After(u.UpdatedAt) || updated.UpdatedAt.Equal(u.UpdatedAt))
		assert.True(t, updated.KhmerNumerals)
	})

	t.Run("audit log is append-only", func(t *testing.T) {
		entry, err := q.InsertAuditLog(ctx, store.InsertAuditLogParams{
			ActorType: "system", Action: "test.run", ResourceType: "test", Metadata: []byte(`{}`),
		})
		require.NoError(t, err)

		_, err = pool.Exec(ctx, "UPDATE audit_log SET action = 'tampered' WHERE id = $1", entry.ID)
		assert.ErrorContains(t, err, "append-only")
		_, err = pool.Exec(ctx, "DELETE FROM audit_log WHERE id = $1", entry.ID)
		assert.ErrorContains(t, err, "append-only")
		_, err = pool.Exec(ctx, "TRUNCATE audit_log")
		assert.ErrorContains(t, err, "append-only")
	})

	t.Run("application role cannot rewrite the audit log", func(t *testing.T) {
		priv := func(table, p string) bool {
			var ok bool
			require.NoError(t, pool.QueryRow(ctx, "SELECT has_table_privilege('opshub_app', $1, $2)", table, p).Scan(&ok))
			return ok
		}
		assert.True(t, priv("audit_log", "INSERT"))
		assert.True(t, priv("audit_log", "SELECT"))
		assert.False(t, priv("audit_log", "UPDATE"))
		assert.False(t, priv("audit_log", "DELETE"))
		assert.False(t, priv("audit_log", "TRUNCATE"))
		assert.True(t, priv("users", "UPDATE"), "other tables keep DML")
	})

	t.Run("sessions store UTC", func(t *testing.T) {
		var tz string
		require.NoError(t, pool.QueryRow(ctx, "SHOW timezone").Scan(&tz))
		assert.Equal(t, "UTC", tz)
	})

	// Down must fully reverse Up so every migration stays reversible.
	pool.Close()
	require.NoError(t, m.Down())
	v, _, err := m.Version()
	require.NoError(t, err)
	assert.Zero(t, v)
	assertNoTables(t, dsn)
}

func assertNoTables(t *testing.T, dsn string) {
	t.Helper()
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()
	var n int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.tables
		 WHERE table_schema = 'public' AND table_name <> 'schema_migrations'`).Scan(&n))
	assert.Zero(t, n, "tables left after down migration")
}
