package seed

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/opshub/opshub/internal/alert"
	"github.com/opshub/opshub/internal/authn"
	"github.com/opshub/opshub/internal/crypto"
	"github.com/opshub/opshub/internal/database"
	"github.com/opshub/opshub/internal/i18n"
	"github.com/opshub/opshub/internal/monitor"
	"github.com/opshub/opshub/internal/notify"
	"github.com/opshub/opshub/internal/store"
)

// noJobs: creating rules and channels enqueues nothing.
type noJobs struct{}

func (noJobs) InsertTx(context.Context, pgx.Tx, river.JobArgs, *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	return &rivertype.JobInsertResult{Job: &rivertype.JobRow{}}, nil
}

// seedMonitoring creates demo monitors (one unreachable on purpose, so an alert fires),
// alert rules and an email channel to the demo owner and admin (Mailpit in development).
// It does nothing when the organization already has monitors.
func seedMonitoring(ctx context.Context, pool *pgxpool.Pool, keys *crypto.KeyRing, out io.Writer) error {
	if keys == nil {
		return nil
	}
	var orgID uuid.UUID
	err := pool.QueryRow(ctx, "SELECT id FROM organizations WHERE slug = $1", DemoOrgSlug).Scan(&orgID)
	if database.IsNoRows(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var n int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM monitors WHERE organization_id = $1", orgID).Scan(&n); err != nil || n > 0 {
		return err
	}
	var admin uuid.UUID
	if err := pool.QueryRow(ctx, "SELECT id FROM users WHERE email = $1", "admin@demo.opshub.local").Scan(&admin); err != nil {
		return err
	}
	actx := authn.WithPrincipal(ctx, authn.Principal{Kind: authn.KindSession, UserID: admin, SessionID: uuid.New()})
	logger := slog.New(slog.DiscardHandler)
	bundle, err := i18n.NewBundle()
	if err != nil {
		return err
	}

	monitors := monitor.NewService(pool, monitor.Config{}, logger)
	for _, in := range []monitor.MonitorInput{
		{Name: "Website · គេហទំព័រ", Kind: store.MonitorKindHttp, Target: "https://example.com/", Labels: []string{"prod", "web"}},
		{Name: "example.com TLS", Kind: store.MonitorKindSsl, Target: "example.com", IntervalSeconds: 3600, Labels: []string{"prod"}},
		// Doesn't resolve: shows a down monitor and a firing alert.
		{Name: "Payments DB (demo, unreachable)", Kind: store.MonitorKindTcp, Target: "payments-db.demo.invalid:5432", Labels: []string{"prod", "db"}},
	} {
		if _, err := monitors.CreateMonitor(actx, orgID, in); err != nil {
			return fmt.Errorf("seed monitor %s: %w", in.Name, err)
		}
	}

	channels := notify.NewService(pool, keys, bundle, nil, notify.Config{DefaultLocale: "en"}, logger)
	ch, err := channels.CreateChannel(actx, orgID, notify.ChannelInput{
		Name: "On-call email", Kind: notify.KindEmail,
		Config: notify.ChannelConfig{Addresses: []string{"owner@demo.opshub.local", "admin@demo.opshub.local"}},
	})
	if err != nil {
		return err
	}
	rules := alert.NewService(pool, noJobs{}, logger)
	notifyOnCall := []alert.Step{{AfterMinutes: 0, ChannelIDs: []uuid.UUID{ch.ID}}}
	cpu, certDays := 90.0, 14.0
	for _, in := range []alert.RuleInput{
		{Name: "Production down · ផលិតកម្មដាច់", Kind: store.AlertRuleKindMonitorDown, Label: ptr("prod"), ForSeconds: 60,
			Severity: store.AlertSeverityCritical, Escalation: notifyOnCall},
		{Name: "Server CPU high", Kind: store.AlertRuleKindAssetMetric, Metric: ptr("cpu"), Threshold: &cpu, ForSeconds: 300,
			Severity: store.AlertSeverityWarning, Escalation: notifyOnCall},
		{Name: "Certificates expiring", Kind: store.AlertRuleKindCertificate, Threshold: &certDays, Severity: store.AlertSeverityWarning},
	} {
		if _, err := rules.CreateRule(actx, orgID, in); err != nil {
			return fmt.Errorf("seed alert rule %s: %w", in.Name, err)
		}
	}
	_, _ = fmt.Fprintf(out, "seed: created 3 demo monitors, 3 alert rules and an email channel (Mailpit)\n")
	return nil
}

func ptr[T any](v T) *T { return &v }
