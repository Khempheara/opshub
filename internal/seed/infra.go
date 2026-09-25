package seed

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/opshub/opshub/internal/crypto"
	"github.com/opshub/opshub/internal/database"
	"github.com/opshub/opshub/internal/store"
)

// demoAssets is the Module 7 inventory of the demo organization.
var demoAssets = []struct {
	kind        store.AssetKind
	name, addr  string
	description string
	tags        []string
	metadata    map[string]string
}{
	{store.AssetKindServer, "web-1", "10.0.1.11", "Payments API · ម៉ាស៊ីនមេ API ទូទាត់", []string{"prod", "web"}, map[string]string{"provider": "hetzner", "region": "sg"}},
	{store.AssetKindServer, "build-1", "10.0.1.21", "CI runner host · ម៉ាស៊ីនសម្រាប់ runner", []string{"ci"}, nil},
	{store.AssetKindDatabase, "payments-db", "db.internal:5432", "PostgreSQL 17 primary", []string{"prod", "postgres"}, map[string]string{"engine": "postgresql"}},
	{store.AssetKindCluster, "prod-k8s", "https://k8s.internal:6443", "Production cluster · ចង្កោមផលិតកម្ម", []string{"prod"}, nil},
	{store.AssetKindDomain, "example.com", "example.com", "Demo domain for certificate checks", []string{"demo"}, nil},
}

// seedInfra creates the demo assets and gives web-1 an agent with a day of sample
// metrics. It does nothing when the organization already has assets.
func seedInfra(ctx context.Context, pool *pgxpool.Pool, out io.Writer) error {
	var orgID uuid.UUID
	err := pool.QueryRow(ctx, "SELECT id FROM organizations WHERE slug = $1", DemoOrgSlug).Scan(&orgID)
	if database.IsNoRows(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var n int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM infra_assets WHERE organization_id = $1", orgID).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		_, _ = fmt.Fprintf(out, "seed: organization already has %d assets; skipping infrastructure\n", n)
		return nil
	}
	return database.InTx(ctx, pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		var web uuid.UUID
		for _, a := range demoAssets {
			md := a.metadata
			if md == nil {
				md = map[string]string{}
			}
			raw, err := json.Marshal(md)
			if err != nil {
				return err
			}
			row, err := q.CreateAsset(ctx, store.CreateAssetParams{
				OrganizationID: orgID, Kind: a.kind, Name: a.name, Address: a.addr, Description: a.description,
				Tags: a.tags, Metadata: raw, TlsPort: 443,
			})
			if err != nil {
				return fmt.Errorf("seed asset %s: %w", a.name, err)
			}
			if a.name == "web-1" {
				web = row.ID
			}
		}
		// A placeholder agent: its token is random and never shown, so it reports once
		// (here) and then shows as offline until someone rotates the token.
		if _, err := tx.Exec(ctx, `UPDATE infra_assets SET agent_token_hash = $2, agent_token_prefix = 'demo',
			agent_version = 'seed', agent_hostname = 'web-1', agent_os = 'linux', agent_arch = 'amd64',
			last_heartbeat_at = now(), last_metrics = '{"cpu_pct": 23.5, "mem_pct": 61.2, "disk_pct": 48.0, "load1": 0.42}'
			WHERE id = $1`, web, crypto.HashToken(crypto.RandomToken(32))); err != nil {
			return err
		}
		// One point a minute for the last day with a daily curve, limited to this month's
		// partition (older partitions may not exist on a fresh database).
		if _, err := tx.Exec(ctx, `INSERT INTO asset_metrics (asset_id, ts, cpu_pct, mem_pct, disk_pct, load1)
			SELECT $1, ts,
				(30 + 20 * sin(extract(epoch FROM ts) / 13751.0) + random() * 15)::real,
				(55 + 8 * sin(extract(epoch FROM ts) / 21600.0) + random() * 4)::real,
				(47 + extract(epoch FROM ts - (now() - interval '1 day')) / 86400.0)::real,
				(0.3 + random() * 0.6)::real
			FROM generate_series(date_trunc('minute', now()) - interval '1 day', date_trunc('minute', now()), interval '1 minute') AS ts
			WHERE ts >= date_trunc('month', now())
			ON CONFLICT DO NOTHING`, web); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(out, "seed: created %d demo infrastructure assets (web-1 has a day of sample metrics)\n", len(demoAssets))
		return nil
	})
}
