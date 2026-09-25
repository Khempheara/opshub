package seed

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/opshub/opshub/internal/authn"
	"github.com/opshub/opshub/internal/crypto"
	"github.com/opshub/opshub/internal/database"
	"github.com/opshub/opshub/internal/secret"
	"github.com/opshub/opshub/internal/store"
)

// seedSecrets gives the demo project a project-wide secret and a per-environment one, created
// by the demo admin. Values are placeholders. It does nothing when the project has secrets.
func seedSecrets(ctx context.Context, pool *pgxpool.Pool, keys *crypto.KeyRing, out io.Writer) error {
	if keys == nil {
		return nil
	}
	var projectID uuid.UUID
	err := pool.QueryRow(ctx, `SELECT p.id FROM projects p JOIN organizations o ON o.id = p.organization_id
		WHERE o.slug = $1 AND p.slug = $2 AND p.deleted_at IS NULL`, DemoOrgSlug, DemoProjectSlug).Scan(&projectID)
	if database.IsNoRows(err) {
		return nil
	}
	if err != nil {
		return err
	}
	n, err := store.New(pool).CountSecrets(ctx, projectID)
	if err != nil || n > 0 {
		return err
	}
	var admin uuid.UUID
	if err := pool.QueryRow(ctx, "SELECT id FROM users WHERE email = $1", "admin@demo.opshub.local").Scan(&admin); err != nil {
		return err
	}
	actx := authn.WithPrincipal(ctx, authn.Principal{Kind: authn.KindSession, UserID: admin, SessionID: uuid.New()})
	svc := secret.NewService(pool, keys, slog.New(slog.DiscardHandler))
	envs := map[string]uuid.UUID{}
	rows, err := pool.Query(ctx, "SELECT name, id FROM environments WHERE project_id = $1 AND deleted_at IS NULL", projectID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var name string
		var id uuid.UUID
		if err := rows.Scan(&name, &id); err != nil {
			rows.Close()
			return err
		}
		envs[name] = id
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	demo := []secret.CreateInput{
		{Name: "SENTRY_DSN", Description: "Error reporting · ការរាយការណ៍កំហុស", Value: "https://demo-key@sentry.example.com/1"},
	}
	for _, env := range []string{"staging", "production"} {
		if id, ok := envs[env]; ok {
			demo = append(demo, secret.CreateInput{
				Name: "DATABASE_URL", EnvironmentID: &id, Description: "Payments database · មូលដ្ឋានទិន្នន័យទូទាត់",
				Value: "postgres://payments:demo-" + env + "@db." + env + ".internal:5432/payments",
			})
		}
	}
	for _, in := range demo {
		if _, err := svc.Create(actx, projectID, in); err != nil {
			return fmt.Errorf("seed secret %s: %w", in.Name, err)
		}
	}
	_, _ = fmt.Fprintf(out, "seed: created %d demo secrets\n", len(demo))
	return nil
}
