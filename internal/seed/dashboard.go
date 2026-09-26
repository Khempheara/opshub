package seed

import (
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/opshub/opshub/internal/database"
	"github.com/opshub/opshub/internal/store"
)

// DemoHistoryProjectSlug holds 60 days of demo delivery history for the dashboard. It is a
// separate project so the payments-api demo (and the tests that use it) stays as it is.
const DemoHistoryProjectSlug = "checkout-web"

const historyDays = 60

var historyTitles = []string{
	"Add KHQR payment option", "Fix cart total rounding", "Update Khmer translations", "Speed up product search",
	"Bump Go to 1.26", "Show delivery time in Phnom Penh time", "Refactor checkout form", "Add order history page",
	"Fix flaky payment test", "Cache exchange rates for 5 minutes",
}

// seedHistory creates the checkout-web project with 60 days of pipeline runs and production
// deployments (with some failures, automatic reverts and a rollback), so the dashboard's
// charts and DORA metrics have something to show. It does nothing when the project exists.
func seedHistory(ctx context.Context, pool *pgxpool.Pool, out io.Writer) error {
	var orgID uuid.UUID
	err := pool.QueryRow(ctx, "SELECT id FROM organizations WHERE slug = $1", DemoOrgSlug).Scan(&orgID)
	if database.IsNoRows(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var exists bool
	if err := pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM projects WHERE organization_id = $1 AND slug = $2 AND deleted_at IS NULL)",
		orgID, DemoHistoryProjectSlug).Scan(&exists); err != nil || exists {
		return err
	}
	// Demo data only: a fixed seed gives the same history every time.
	rng := rand.New(rand.NewPCG(2026, 11)) // #nosec G404 -- not security relevant
	now := time.Now().UTC()
	runs, deploys := 0, 0
	err = database.InTx(ctx, pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		var projectID, prodID, stagingID, admin uuid.UUID
		if err := tx.QueryRow(ctx, "SELECT id FROM users WHERE email = 'admin@demo.opshub.local'").Scan(&admin); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO projects (organization_id, slug, name, description, created_by)
			VALUES ($1, $2, 'Checkout Web · ទំព័រទូទាត់', 'Demo delivery history for the dashboard · ប្រវត្តិសម្រាប់ផ្ទាំងគ្រប់គ្រង', $3) RETURNING id`,
			orgID, DemoHistoryProjectSlug, admin).Scan(&projectID); err != nil {
			return err
		}
		for _, e := range []struct {
			name, kind string
			dst        *uuid.UUID
		}{{"staging", "staging", &stagingID}, {"production", "production", &prodID}} {
			if err := tx.QueryRow(ctx, "INSERT INTO environments (project_id, name, kind) VALUES ($1, $2, $3) RETURNING id",
				projectID, e.name, e.kind).Scan(e.dst); err != nil {
				return err
			}
		}
		if err := seedAccess(ctx, q, tx, orgID, projectID); err != nil {
			return err
		}
		var current *uuid.UUID
		version := 100
		deploy := func(env uuid.UUID, run *uuid.UUID, status string, reverted bool, rollbackOf *uuid.UUID, created time.Time, took time.Duration) (uuid.UUID, error) {
			deploys++
			var id uuid.UUID
			err := tx.QueryRow(ctx, `INSERT INTO deployments (organization_id, project_id, environment_id, number, target_name, target_kind,
				version, strategy, status, reverted, run_id, rollback_of_id, created_by, created_at, started_at, finished_at)
				VALUES ($1, $2, $3, $4, 'demo-k8s', 'kubernetes', $5, 'rolling', $6, $7, $8, $9, $10, $11, $11, $12) RETURNING id`,
				orgID, projectID, env, deploys, fmt.Sprintf("ghcr.io/angkor-tech/checkout-web:1.%d.0", version), status, reverted,
				run, rollbackOf, admin, created, created.Add(took)).Scan(&id)
			return id, err
		}
		for day := historyDays; day >= 1; day-- {
			start := now.AddDate(0, 0, -day).Truncate(24 * time.Hour).Add(2 * time.Hour) // 09:00 in Phnom Penh
			weekend := start.Weekday() == time.Saturday || start.Weekday() == time.Sunday
			n := 2 + rng.IntN(5)
			if weekend {
				n = rng.IntN(2)
			}
			for i := range n {
				runs++
				created := start.Add(time.Duration(i*90+rng.IntN(60)) * time.Minute)
				committed := created.Add(-time.Duration(20+rng.IntN(360)) * time.Minute)
				took := time.Duration(180+rng.IntN(540)) * time.Second
				status := "succeeded"
				switch r := rng.IntN(100); {
				case r < 12:
					status = "failed"
				case r < 15:
					status = "canceled"
				}
				var runID uuid.UUID
				if err := tx.QueryRow(ctx, `INSERT INTO pipeline_runs (organization_id, project_id, number, status, trigger, ref, commit_sha,
					title, actor_name, created_at, started_at, finished_at, committed_at)
					VALUES ($1, $2, $3, $4, 'push', 'refs/heads/main', $5, $6, 'dara-kim', $7, $8, $9, $10) RETURNING id`,
					orgID, projectID, runs, status, fmt.Sprintf("%07x", rng.Uint32()), historyTitles[rng.IntN(len(historyTitles))],
					created, created.Add(20*time.Second), created.Add(took), committed).Scan(&runID); err != nil {
					return err
				}
				// About half the successful runs on weekdays go to production.
				if status != "succeeded" || weekend || rng.IntN(2) == 0 {
					continue
				}
				version++
				at := created.Add(took + time.Duration(5+rng.IntN(40))*time.Minute)
				if _, err := deploy(stagingID, &runID, "succeeded", false, nil, at, 2*time.Minute); err != nil {
					return err
				}
				at = at.Add(time.Duration(15+rng.IntN(120)) * time.Minute)
				switch r := rng.IntN(100); {
				case r < 8: // failed and reverted itself
					if _, err := deploy(prodID, &runID, "failed", true, nil, at, 4*time.Minute); err != nil {
						return err
					}
				case r < 12: // failed; the next release fixes it
					if _, err := deploy(prodID, &runID, "failed", false, nil, at, 3*time.Minute); err != nil {
						return err
					}
				case r < 16: // went live, rolled back soon after
					bad, err := deploy(prodID, &runID, "succeeded", false, nil, at, 3*time.Minute)
					if err != nil {
						return err
					}
					if _, err := deploy(prodID, nil, "succeeded", false, &bad, at.Add(time.Duration(10+rng.IntN(30))*time.Minute), 2*time.Minute); err != nil {
						return err
					}
				default:
					id, err := deploy(prodID, &runID, "succeeded", false, nil, at, 3*time.Minute)
					if err != nil {
						return err
					}
					current = &id
				}
			}
		}
		if current != nil {
			if _, err := tx.Exec(ctx, "UPDATE environments SET current_deployment_id = $1 WHERE id = $2", *current, prodID); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, "UPDATE projects SET last_run_number = $2, last_deployment_number = $3 WHERE id = $1", projectID, runs, deploys)
		return err
	})
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "seed: created %s with %d days of history (%d runs, %d deployments)\n", DemoHistoryProjectSlug, historyDays, runs, deploys)
	return nil
}
