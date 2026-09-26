package seed

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/opshub/opshub/internal/authn"
	"github.com/opshub/opshub/internal/database"
	"github.com/opshub/opshub/internal/logs"
)

// demoLogLines are a checkout service's recent output, in English and Khmer.
var demoLogLines = []struct {
	level, message string
	attrs          map[string]any
}{
	{"info", "checkout service started", map[string]any{"version": "1.4.2", "port": 8080}},
	{"info", "GET /api/cart 200", map[string]any{"path": "/api/cart", "status": 200, "ms": 12}},
	{"debug", "cache hit cart:5821", map[string]any{"key": "cart:5821"}},
	{"info", "POST /api/checkout 201", map[string]any{"path": "/api/checkout", "status": 201, "ms": 184, "order": 10231}},
	{"warn", "payment gateway slow response", map[string]any{"gateway": "aba", "ms": 2310}},
	{"info", "ការបញ្ជាទិញ 10232 ត្រូវបានបង់ប្រាក់រួចរាល់", map[string]any{"order": 10232, "currency": "KHR"}},
	{"error", "payment gateway timeout after 5s", map[string]any{"gateway": "aba", "order": 10233, "attempt": 1}},
	{"info", "retrying payment for order 10233", map[string]any{"order": 10233, "attempt": 2}},
	{"info", "POST /api/checkout 201", map[string]any{"path": "/api/checkout", "status": 201, "ms": 402, "order": 10233}},
	{"warn", "ស្តុកទំនិញជិតអស់៖ កាហ្វេកំពត", map[string]any{"sku": "KAMPOT-COFFEE-250", "left": 3}},
	{"info", "GET /healthz 200", map[string]any{"path": "/healthz", "status": 200, "ms": 1}},
	{"error", "database connection reset by peer", map[string]any{"db": "payments", "retry_in_ms": 500}},
	{"info", "database connection restored", map[string]any{"db": "payments"}},
}

// seedLogs creates an ingest token for a demo service and sends it a few recent lines. The
// token itself is not printed (create one in the UI to send your own lines). It does nothing
// when the organization already has ingest tokens.
func seedLogs(ctx context.Context, pool *pgxpool.Pool, retentionDays int, out io.Writer) error {
	var orgID uuid.UUID
	err := pool.QueryRow(ctx, "SELECT id FROM organizations WHERE slug = $1", DemoOrgSlug).Scan(&orgID)
	if database.IsNoRows(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var n int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM log_ingest_tokens WHERE organization_id = $1", orgID).Scan(&n); err != nil || n > 0 {
		return err
	}
	var admin uuid.UUID
	if err := pool.QueryRow(ctx, "SELECT id FROM users WHERE email = $1", "admin@demo.opshub.local").Scan(&admin); err != nil {
		return err
	}
	actx := authn.WithPrincipal(ctx, authn.Principal{Kind: authn.KindSession, UserID: admin, SessionID: uuid.New()})
	svc := logs.NewService(pool, logs.Config{RetentionDays: retentionDays}, slog.New(slog.DiscardHandler))
	if err := svc.Maintain(ctx); err != nil {
		return err
	}
	tok, err := svc.CreateToken(actx, orgID, logs.TokenInput{Name: "Checkout service", Service: DemoProjectSlug + "/checkout"})
	if err != nil {
		return err
	}
	st, err := svc.Authenticate(ctx, tok.Token)
	if err != nil {
		return err
	}
	// Spread over the last 30 minutes, oldest first.
	start := time.Now().UTC().Add(-30 * time.Minute)
	step := 30 * time.Minute / time.Duration(len(demoLogLines))
	var b strings.Builder
	for i, l := range demoLogLines {
		line := map[string]any{"ts": start.Add(time.Duration(i) * step).Format(time.RFC3339Nano), "level": l.level, "message": l.message}
		for k, v := range l.attrs {
			line[k] = v
		}
		raw, err := json.Marshal(line)
		if err != nil {
			return err
		}
		b.Write(raw)
		b.WriteByte('\n')
	}
	res, err := svc.Ingest(ctx, st, strings.NewReader(b.String()))
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "seed: created ingest token %s… for %s and %d log lines\n", tok.TokenPrefix, tok.Service, res.Accepted)
	return nil
}
