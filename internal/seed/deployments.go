package seed

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/opshub/opshub/internal/crypto"
	"github.com/opshub/opshub/internal/database"
)

// DemoTargetName is the seeded deploy target. Its kubeconfig points at a host that doesn't
// resolve, so deployments started in the demo fail safely as "target unreachable".
const DemoTargetName = "demo-k8s"

// demoKubeconfig builds a syntactically valid kubeconfig for an unreachable cluster.
func demoKubeconfig() (string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "demo-ca"}, IsCA: true, BasicConstraintsValid: true,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(1, 0, 0), KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return "", err
	}
	ca := base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	return `apiVersion: v1
kind: Config
current-context: demo
clusters:
- name: demo
  cluster:
    server: https://k8s.demo.opshub.invalid:6443
    certificate-authority-data: ` + ca + `
users:
- name: opshub
  user:
    token: demo-token-not-a-real-cluster
contexts:
- name: demo
  context: {cluster: demo, user: opshub, namespace: payments}
`, nil
}

type demoDeployment struct {
	env, version, previous, status, reason string
	reverted                               bool
	ago                                    time.Duration
	log                                    string
}

// seedDeployments adds a demo Kubernetes target and a short release history to the demo
// project when it has no deployments yet.
func seedDeployments(ctx context.Context, pool *pgxpool.Pool, keys *crypto.KeyRing, out io.Writer) error {
	if keys == nil {
		return nil
	}
	var projectID, orgID uuid.UUID
	err := pool.QueryRow(ctx, `SELECT p.id, p.organization_id FROM projects p JOIN organizations o ON o.id = p.organization_id
		WHERE o.slug = $1 AND p.slug = $2 AND p.deleted_at IS NULL`, DemoOrgSlug, DemoProjectSlug).Scan(&projectID, &orgID)
	if database.IsNoRows(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var n int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM deployments WHERE project_id = $1", projectID).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		_, _ = fmt.Fprintf(out, "seed: project %q already has deployments; skipping\n", DemoProjectSlug)
		return nil
	}
	var admin uuid.UUID
	if err := pool.QueryRow(ctx, "SELECT id FROM users WHERE email = 'admin@demo.opshub.local'").Scan(&admin); err != nil {
		return err
	}
	kubeconfig, err := demoKubeconfig()
	if err != nil {
		return err
	}
	creds, _ := json.Marshal(map[string]string{"kubeconfig": kubeconfig})
	config, _ := json.Marshal(map[string]any{
		"namespace": "payments", "deployment": "payments-api", "service": "payments-api", "rollout_timeout_seconds": 300,
	})
	history := []demoDeployment{
		{env: "staging", version: "ghcr.io/angkor-tech/payments-api:1.3.0", status: "succeeded", ago: 72 * time.Hour,
			log: "Deploying ghcr.io/angkor-tech/payments-api:1.3.0 to staging on demo-k8s (kubernetes, rolling)\npayments/payments-api: 2 of 2 updated, 2 available\n\x1b[32;1mDeployed ghcr.io/angkor-tech/payments-api:1.3.0 to demo-k8s.\x1b[0m\n"},
		{env: "production", version: "ghcr.io/angkor-tech/payments-api:1.3.0", status: "succeeded", ago: 70 * time.Hour,
			log: "Deploying ghcr.io/angkor-tech/payments-api:1.3.0 to production on demo-k8s (kubernetes, rolling)\npayments/payments-api: 4 of 4 updated, 4 available\n\x1b[32;1mDeployed ghcr.io/angkor-tech/payments-api:1.3.0 to demo-k8s.\x1b[0m\n"},
		{env: "production", version: "ghcr.io/angkor-tech/payments-api:1.4.0", previous: "ghcr.io/angkor-tech/payments-api:1.3.0", status: "succeeded", ago: 26 * time.Hour,
			log: "Deploying ghcr.io/angkor-tech/payments-api:1.4.0 to production on demo-k8s (kubernetes, rolling)\nCurrent release: ghcr.io/angkor-tech/payments-api:1.3.0\npayments/payments-api: 4 of 4 updated, 4 available\n\x1b[32;1mDeployed ghcr.io/angkor-tech/payments-api:1.4.0 to demo-k8s.\x1b[0m\n"},
		{env: "staging", version: "ghcr.io/angkor-tech/payments-api:1.4.1", previous: "ghcr.io/angkor-tech/payments-api:1.3.0", status: "failed",
			reason: "health_check_failed", reverted: true, ago: 3 * time.Hour,
			log: "Deploying ghcr.io/angkor-tech/payments-api:1.4.1 to staging on demo-k8s (kubernetes, rolling)\npayments/payments-api: 1 of 2 updated, 1 available\n\x1b[31;1mDeployment failed: rollout of payments-api stalled: CrashLoopBackOff\x1b[0m\nThe previous release was restored.\n"},
	}
	return database.InTx(ctx, pool, func(tx pgx.Tx) error {
		targetID, err := uuid.NewV7()
		if err != nil {
			return err
		}
		enc, err := keys.Encrypt(creds, targetID[:])
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO deploy_targets (id, organization_id, name, kind, description, config, credentials_enc, created_by)
			VALUES ($1, $2, $3, 'kubernetes', 'Demo cluster (not reachable: deployments fail safely)', $4, $5, $6)`,
			targetID, orgID, DemoTargetName, config, enc, admin); err != nil {
			return err
		}
		health := `[{"target":"rollout payments-api","ok":true,"attempts":1,"detail":"ready in 42s","at":"2026-01-01T00:00:00Z"}]`
		failedHealth := `[{"target":"rollout payments-api","ok":false,"attempts":1,"detail":"rollout of payments-api stalled: CrashLoopBackOff","at":"2026-01-01T00:00:00Z"}]`
		for i, d := range history {
			var envID uuid.UUID
			if err := tx.QueryRow(ctx, "SELECT id FROM environments WHERE project_id = $1 AND name = $2 AND deleted_at IS NULL", projectID, d.env).Scan(&envID); err != nil {
				return err
			}
			created := time.Now().Add(-d.ago)
			h := health
			var reason *string
			if d.status == "failed" {
				h, reason = failedHealth, &d.reason
			}
			var id uuid.UUID
			if err := tx.QueryRow(ctx, `INSERT INTO deployments (organization_id, project_id, environment_id, number, target_id, target_name,
				target_kind, version, previous_version, strategy, status, failure_reason, reverted, health, created_by, started_at,
				finished_at, created_at, log_bytes)
				VALUES ($1, $2, $3, $4, $5, $6, 'kubernetes', $7, $8, 'rolling', $9, $10, $11, $12, $13, $14, $15, $14, $16) RETURNING id`,
				orgID, projectID, envID, i+1, targetID, DemoTargetName, d.version, d.previous, d.status, reason, d.reverted, h, admin,
				created, created.Add(95*time.Second), len(d.log)).Scan(&id); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, "INSERT INTO deployment_log_chunks (deployment_id, seq, content) VALUES ($1, 0, $2)", id, d.log); err != nil {
				return err
			}
			if d.status == "succeeded" {
				if _, err := tx.Exec(ctx, "UPDATE environments SET current_deployment_id = $1 WHERE id = $2", id, envID); err != nil {
					return err
				}
			}
		}
		if _, err := tx.Exec(ctx, "UPDATE projects SET last_deployment_number = $1 WHERE id = $2", len(history), projectID); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(out, "seed: created deploy target %q and %d demo deployments\n", DemoTargetName, len(history))
		return nil
	})
}
