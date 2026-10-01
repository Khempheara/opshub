package seed

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/opshub/opshub/internal/alert"
	"github.com/opshub/opshub/internal/authn"
	"github.com/opshub/opshub/internal/crypto"
	"github.com/opshub/opshub/internal/database"
	"github.com/opshub/opshub/internal/i18n"
	"github.com/opshub/opshub/internal/logs"
	"github.com/opshub/opshub/internal/monitor"
	"github.com/opshub/opshub/internal/notify"
	"github.com/opshub/opshub/internal/store"
)

// infrastructure adds servers (one reporting, one whose agent went quiet, one without an
// agent), a cluster, databases and domains whose certificates are valid, expiring, expired
// and failing. Certificate checks are scheduled weeks ahead so the states stay as seeded.
func (s *sc) infrastructure() error {
	ctx := s.ctx
	type asset struct {
		kind, name, addr, description string
		tags                          []string
		metadata                      map[string]string
		heartbeat                     *time.Time
		metrics                       map[string]float64
	}
	list := []asset{
		{"server", "api-1", "10.20.0.11", "Mobile API · ម៉ាស៊ីនមេ API", []string{"prod", "api"}, map[string]string{"provider": "aws", "region": "ap-southeast-1", "size": "c7g.large"},
			at(s.now.Add(-15 * time.Second)), map[string]float64{"cpu_pct": 41.5, "mem_pct": 66.1, "disk_pct": 86.4, "load1": 1.21}},
		{"server", "api-2", "10.20.0.12", "Mobile API · ម៉ាស៊ីនមេ API", []string{"prod", "api"}, map[string]string{"provider": "aws", "region": "ap-southeast-1", "size": "c7g.large"},
			at(s.now.Add(-3 * time.Hour)), map[string]float64{"cpu_pct": 97.2, "mem_pct": 91.8, "disk_pct": 62.0, "load1": 7.9}},
		{"server", "worker-1", "10.20.0.21", "Report workers (agent not installed yet)", []string{"prod", "jobs"}, nil, nil, nil},
		{"cluster", "mekong-k8s", "https://k8s.mekong.example:6443", "EKS 1.33 · ចង្កោម", []string{"prod"}, map[string]string{"provider": "aws", "version": "1.33"}, nil, nil},
		{"database", "orders-db", "orders-db.internal:5432", "PostgreSQL 17 primary", []string{"prod", "postgres"}, map[string]string{"engine": "postgresql", "version": "17"}, nil, nil},
		{"database", "cache-1", "cache.internal:6379", "Redis for sessions", []string{"prod", "redis"}, map[string]string{"engine": "redis"}, nil, nil},
		{"domain", "mekong.example", "mekong.example", "Marketing site", []string{"prod", "web"}, nil, nil, nil},
		{"domain", "api.mekong.example", "api.mekong.example", "Public API · API សាធារណៈ", []string{"prod", "api"}, nil, nil, nil},
		{"domain", "old.mekong.example", "old.mekong.example", "Being retired · កំពុងឈប់ប្រើ", []string{"legacy"}, nil, nil, nil},
		{"domain", "status.mekong.invalid", "status.mekong.invalid", "Status page (DNS not set up yet)", []string{"web"}, nil, nil, nil},
	}
	certs := map[string]struct {
		issuer string
		from   time.Time
		to     time.Time
		err    string
	}{
		"mekong.example":        {"CN=R11,O=Let's Encrypt,C=US", s.now.AddDate(0, 0, -10), s.now.AddDate(0, 0, 80), ""},
		"api.mekong.example":    {"CN=R11,O=Let's Encrypt,C=US", s.now.AddDate(0, 0, -81), s.now.AddDate(0, 0, 9), ""},
		"old.mekong.example":    {"CN=Sectigo RSA Domain Validation Secure Server CA,O=Sectigo Limited,C=GB", s.now.AddDate(-1, 0, -3), s.now.AddDate(0, 0, -3), ""},
		"status.mekong.invalid": {"", time.Time{}, time.Time{}, "dial tcp: lookup status.mekong.invalid: no such host"},
	}
	return database.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		admin := s.user("admin@demo.opshub.local")
		for i, a := range list {
			md := a.metadata
			if md == nil {
				md = map[string]string{}
			}
			var id uuid.UUID
			if err := tx.QueryRow(ctx, `INSERT INTO infra_assets (organization_id, kind, name, address, description, tags, metadata, tls_port, created_by, created_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, 443, $8, $9) RETURNING id`,
				s.org, a.kind, a.name, a.addr, a.description, a.tags, mustJSON(md), admin, s.now.AddDate(0, 0, -36+i)).Scan(&id); err != nil {
				return fmt.Errorf("asset %s: %w", a.name, err)
			}
			s.assets[a.name] = id
			if a.heartbeat != nil {
				if _, err := tx.Exec(ctx, `UPDATE infra_assets SET agent_token_hash = $2, agent_token_prefix = 'demo', agent_version = '1.4.0',
					agent_hostname = $3, agent_os = 'linux', agent_arch = 'arm64', last_heartbeat_at = $4, last_metrics = $5 WHERE id = $1`,
					id, crypto.HashToken(crypto.RandomToken(32)), a.name, a.heartbeat, mustJSON(a.metrics)); err != nil {
					return err
				}
				if err := s.assetMetrics(tx, id, a.name == "api-2", *a.heartbeat); err != nil {
					return err
				}
			}
			if c, ok := certs[a.name]; ok {
				var subject, issuer, serial, fp string
				var from, to *time.Time
				names := []string{}
				if c.err == "" {
					subject, issuer = "CN="+a.name, c.issuer
					serial, fp = fmt.Sprintf("%032x", 0x5eed0000+i), strings.Repeat(fmt.Sprintf("%02X:", 0x40+i), 31)+"7F"
					from, to, names = &c.from, &c.to, []string{a.name, "www." + a.name}
				}
				if _, err := tx.Exec(ctx, `INSERT INTO ssl_certificates (asset_id, host, port, subject, issuer, dns_names, serial, fingerprint,
					not_before, not_after, error, last_checked_at, next_check_at)
					VALUES ($1, $2, 443, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
					id, a.addr, subject, issuer, names, serial, fp, from, to, c.err, s.now.Add(-50*time.Minute), s.now.AddDate(0, 0, 21)); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// assetMetrics writes a day of per-minute samples (this month's partition only: older ones may
// not exist on a fresh database) and 30 days of hourly rollups. A quiet agent stops at its
// last heartbeat; api-2's CPU climbs before it went quiet.
func (s *sc) assetMetrics(tx pgx.Tx, id uuid.UUID, quiet bool, last time.Time) error {
	ctx := s.ctx
	hot := 0.0
	if quiet {
		hot = 1
	}
	if _, err := tx.Exec(ctx, `INSERT INTO asset_metrics (asset_id, ts, cpu_pct, mem_pct, disk_pct, load1)
		SELECT $1, ts,
			least(99, 35 + 18 * sin(extract(epoch FROM ts) / 13751.0) + random() * 10
				+ $3 * greatest(0, 60 - extract(epoch FROM $2::timestamptz - ts) / 60))::real,
			(60 + 6 * sin(extract(epoch FROM ts) / 21600.0) + random() * 3 + $3 * 25)::real,
			(85 + extract(epoch FROM ts - ($2::timestamptz - interval '1 day')) / 86400.0 * 1.4 - $3 * 23)::real,
			(0.8 + random() * 0.8 + $3 * 5)::real
		FROM generate_series(date_trunc('minute', $2::timestamptz) - interval '1 day', date_trunc('minute', $2::timestamptz), interval '1 minute') AS ts
		WHERE ts >= date_trunc('month', now())`, id, last, hot); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO asset_metrics_hourly (asset_id, hour, cpu_avg, cpu_max, mem_avg, mem_max, disk_avg, disk_max, samples)
		SELECT $1, h, c, c + 12 + random() * 8, m, m + 5, d, d + 0.3, 60
		FROM (SELECT h,
				(32 + 15 * sin(extract(epoch FROM h) / 13751.0) + random() * 8)::real AS c,
				(58 + 5 * sin(extract(epoch FROM h) / 21600.0) + random() * 3)::real AS m,
				(70 + extract(epoch FROM h - ($2::timestamptz - interval '30 days')) / 86400.0 * 0.5 - $3 * 23)::real AS d
			FROM generate_series(date_trunc('hour', $2::timestamptz) - interval '30 days', date_trunc('hour', $2::timestamptz) - interval '1 hour', interval '1 hour') AS h) x
		ON CONFLICT DO NOTHING`, id, last, hot)
	return err
}

// monitoring creates monitors with a day of results and 30 days of hourly rollups, channels
// of every kind and rules of every kind. Monitors that check real hosts: Mobile API
// (example.com, up), Orders DB and Staging API (unresolvable: down, so their alerts stay
// open). The others are paused and keep their seeded history.
func (s *sc) monitoring() error {
	ctx := s.ctx
	admin := s.user("admin@demo.opshub.local")
	actx := authn.WithPrincipal(ctx, authn.Principal{Kind: authn.KindSession, UserID: admin, SessionID: uuid.New()})
	logger := slog.New(slog.DiscardHandler)
	off := false
	type mon struct {
		in        monitor.MonitorInput
		downSince time.Duration // > 0: down for this long
		flaky     float64       // share of failed checks in the history
		latency   int
	}
	mons := []mon{
		{monitor.MonitorInput{Name: "Mobile API · API ទូរស័ព្ទ", Kind: store.MonitorKindHttp, Target: "https://example.com/", Labels: []string{"prod", "mobile"}}, 0, 0.004, 180},
		{monitor.MonitorInput{Name: "Orders DB", Kind: store.MonitorKindTcp, Target: "orders-db.mekong.invalid:5432", Labels: []string{"prod", "db"}}, 2 * time.Hour, 0.01, 4},
		{monitor.MonitorInput{Name: "Staging API", Kind: store.MonitorKindTcp, Target: "staging-api.mekong.invalid:443", Labels: []string{"staging"}}, 20 * time.Minute, 0.02, 9},
		{monitor.MonitorInput{Name: "Status page", Kind: store.MonitorKindHttp, Target: "https://status.mekong.example/", Labels: []string{"web"}, Enabled: &off}, 0, 0, 95},
		{monitor.MonitorInput{Name: "mekong.example TLS", Kind: store.MonitorKindSsl, Target: "mekong.example", IntervalSeconds: 3600, Labels: []string{"prod", "web"}, Enabled: &off}, 0, 0, 140},
		{monitor.MonitorInput{Name: "Admin panel (paused)", Kind: store.MonitorKindHttp, Target: "https://admin.mekong.example/login", Labels: []string{"internal"}, Enabled: &off}, 0, 0.06, 420},
	}
	monitors := monitor.NewService(s.pool, monitor.Config{}, logger)
	rng := rand.New(rand.NewPCG(2026, 13)) // #nosec G404 -- demo data
	for _, m := range mons {
		created, err := monitors.CreateMonitor(actx, s.org, m.in)
		if err != nil {
			return fmt.Errorf("monitor %s: %w", m.in.Name, err)
		}
		s.monitors[m.in.Name] = created.ID
		if err := s.monitorHistory(rng, created.ID, m.downSince, m.flaky, m.latency); err != nil {
			return err
		}
	}

	bundle, err := i18n.NewBundle()
	if err != nil {
		return err
	}
	km := "km"
	channels := notify.NewService(s.pool, s.keys, bundle, nil, notify.Config{DefaultLocale: "en"}, logger)
	for _, c := range []struct {
		in     notify.ChannelInput
		tested *time.Time
		ok     *bool
	}{
		{notify.ChannelInput{Name: "On-call email", Kind: notify.KindEmail,
			Config: notify.ChannelConfig{Addresses: []string{"owner@demo.opshub.local", "admin@demo.opshub.local", ShowcaseTwoFactorEmail}}},
			at(s.now.AddDate(0, 0, -20)), ptr(true)},
		{notify.ChannelInput{Name: "Platform leads (Khmer)", Kind: notify.KindEmail, Locale: &km,
			Config: notify.ChannelConfig{Addresses: []string{"owner@demo.opshub.local"}}}, nil, nil},
		{notify.ChannelInput{Name: "#ops-alerts", Kind: store.ChannelKindSlack,
			Secrets: notify.ChannelSecrets{WebhookURL: "https://hooks.slack.com/services/T0DEMO000/B0DEMO000/notARealWebhookSecret0"}},
			at(s.now.AddDate(0, 0, -6)), ptr(false)},
		{notify.ChannelInput{Name: "Telegram on-call", Kind: store.ChannelKindTelegram, Locale: &km,
			Config:  notify.ChannelConfig{ChatID: "-1001234567890"},
			Secrets: notify.ChannelSecrets{BotToken: "123456789:AAdemoTokenNotRealxxxxxxxxxxxxxxxxx"}},
			at(s.now.AddDate(0, 0, -6)), ptr(true)},
		{notify.ChannelInput{Name: "Incident webhook", Kind: store.ChannelKindWebhook,
			Config:  notify.ChannelConfig{URL: "https://hooks.mekong.example/opshub"},
			Secrets: notify.ChannelSecrets{SigningSecret: "demo-signing-secret-not-real"}}, nil, nil},
	} {
		ch, err := channels.CreateChannel(actx, s.org, c.in)
		if err != nil {
			return fmt.Errorf("channel %s: %w", c.in.Name, err)
		}
		s.channels[c.in.Name] = ch.ID
		if _, err := s.pool.Exec(ctx, "UPDATE notification_channels SET last_test_at = $2, last_test_ok = $3 WHERE id = $1", ch.ID, c.tested, c.ok); err != nil {
			return err
		}
	}

	ch := func(names ...string) []uuid.UUID {
		var out []uuid.UUID
		for _, n := range names {
			out = append(out, s.channels[n])
		}
		return out
	}
	onCall := []alert.Step{{AfterMinutes: 0, ChannelIDs: ch("On-call email")}}
	rules := alert.NewService(s.pool, noJobs{}, logger)
	cpu, disk, slow, beta, certDays := 85.0, 90.0, 1500.0, 5000.0, 21.0
	api2 := s.assets["api-2"]
	for _, in := range []alert.RuleInput{
		{Name: "Production down · ផលិតកម្មដាច់", Kind: store.AlertRuleKindMonitorDown, Label: ptr("prod"), ForSeconds: 60, Severity: store.AlertSeverityCritical,
			Escalation: []alert.Step{{AfterMinutes: 0, ChannelIDs: ch("On-call email")}, {AfterMinutes: 15, ChannelIDs: ch("Platform leads (Khmer)")}}},
		{Name: "Staging down", Kind: store.AlertRuleKindMonitorDown, Label: ptr("staging"), ForSeconds: int32(alert.MaxForSeconds), Severity: store.AlertSeverityWarning, Escalation: onCall},
		{Name: "Slow responses", Kind: store.AlertRuleKindMonitorLatency, Label: ptr("prod"), Threshold: &slow, ForSeconds: 300, Severity: store.AlertSeverityWarning, Escalation: onCall},
		{Name: "Server CPU high · CPU ខ្ពស់", Kind: store.AlertRuleKindAssetMetric, Metric: ptr("cpu"), Threshold: &cpu, ForSeconds: 300, Severity: store.AlertSeverityWarning, Escalation: onCall},
		{Name: "Disk almost full", Kind: store.AlertRuleKindAssetMetric, Metric: ptr("disk"), Threshold: &disk, ForSeconds: 600, Severity: store.AlertSeverityCritical, Escalation: onCall},
		{Name: "api-2 agent offline", Kind: store.AlertRuleKindAssetOffline, TargetID: &api2, ForSeconds: 300, Severity: store.AlertSeverityCritical, Escalation: onCall},
		{Name: "Certificates expiring", Kind: store.AlertRuleKindCertificate, Threshold: &certDays, Severity: store.AlertSeverityWarning,
			Escalation: []alert.Step{{AfterMinutes: 0, ChannelIDs: ch("Platform leads (Khmer)")}}},
		{Name: "Beta latency (chat channels)", Kind: store.AlertRuleKindMonitorLatency, Label: ptr("beta"), Threshold: &beta, ForSeconds: 60, Severity: store.AlertSeverityInfo,
			Escalation: []alert.Step{{AfterMinutes: 0, ChannelIDs: ch("#ops-alerts", "Telegram on-call")}, {AfterMinutes: 30, ChannelIDs: ch("Incident webhook")}}},
		{Name: "Legacy site down (turned off)", Kind: store.AlertRuleKindMonitorDown, Label: ptr("legacy"), ForSeconds: 120, Severity: store.AlertSeverityWarning, Enabled: &off},
	} {
		r, err := rules.CreateRule(actx, s.org, in)
		if err != nil {
			return fmt.Errorf("rule %s: %w", in.Name, err)
		}
		s.rules[in.Name] = r.ID
	}
	_, _ = fmt.Fprintf(s.out, "seed: showcase monitoring: %d monitors, 5 channels, 9 rules\n", len(mons))
	return nil
}

// monitorHistory writes a day of checks (every 5 minutes, this month's partition only), 30
// days of hourly rollups, and the monitor's current state.
func (s *sc) monitorHistory(rng *rand.Rand, id uuid.UUID, downFor time.Duration, flaky float64, latency int) error {
	ctx := s.ctx
	return database.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		monthStart := time.Date(s.now.Year(), s.now.Month(), 1, 0, 0, 0, 0, time.UTC)
		var lastLatency *int
		for ts := s.now.Add(-24 * time.Hour).Truncate(5 * time.Minute); !ts.After(s.now); ts = ts.Add(5 * time.Minute) {
			if ts.Before(monthStart) {
				continue
			}
			down := (downFor > 0 && ts.After(s.now.Add(-downFor))) || rng.Float64() < flaky
			var lat, code *int
			errText := ""
			if down {
				errText = "connection failed: host not found"
			} else {
				v := latency + int(float64(latency)*0.4*math.Sin(float64(ts.Unix())/9000)) + rng.IntN(latency/3+1)
				lat, lastLatency = &v, &v
				code = ptr(200)
			}
			if _, err := tx.Exec(ctx, "INSERT INTO monitor_results (monitor_id, ts, up, latency_ms, status_code, error) VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT DO NOTHING",
				id, ts, !down, lat, code, errText); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO monitor_results_hourly (monitor_id, hour, checks, up_checks, latency_avg, latency_max)
			SELECT $1, h, 60, 60 - (CASE WHEN random() < $3 * 20 THEN 1 + floor(random() * 6) ELSE 0 END)::int,
				($2 * (1 + 0.3 * sin(extract(epoch FROM h) / 9000.0)))::real, ($2 * 2.2 + random() * $2)::real
			FROM generate_series(date_trunc('hour', now()) - interval '30 days', date_trunc('hour', now()) - interval '1 hour', interval '1 hour') AS h
			ON CONFLICT DO NOTHING`, id, latency, flaky); err != nil {
			return err
		}
		up := downFor == 0
		var since *time.Time
		lastErr := ""
		if !up {
			since, lastErr, lastLatency = at(s.now.Add(-downFor)), "connection failed: host not found", nil
		}
		_, err := tx.Exec(ctx, `UPDATE monitors SET last_up = $2, last_checked_at = $3, last_latency_ms = $4, last_error = $5, down_since = $6 WHERE id = $1`,
			id, up, s.now.Add(-time.Minute), lastLatency, lastErr, since)
		return err
	})
}

type showcaseEvent struct {
	kind    string
	after   time.Duration // after the alert started
	channel string
	user    string
	detail  string
}

// alerts writes open alerts that match what the rules see (so the evaluator keeps them:
// Orders DB down, api-2's agent quiet and acknowledged, certificates, Staging API pending),
// 30 days of resolved alerts with full timelines, and silences (active, scheduled, ended).
func (s *sc) alerts() error {
	ctx := s.ctx
	return database.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		add := func(rule, severity, subjectType string, subject uuid.UUID, subjectName string, labels []string, status string,
			details map[string]any, started time.Time, resolved *time.Time, ackBy string, ackAfter time.Duration, events []showcaseEvent) error {
			ruleKind := ""
			if err := tx.QueryRow(ctx, "SELECT kind FROM alert_rules WHERE id = $1", s.rules[rule]).Scan(&ruleKind); err != nil {
				return err
			}
			var ack *uuid.UUID
			var ackAt *time.Time
			if ackBy != "" {
				u := s.user(ackBy)
				ack, ackAt = &u, at(started.Add(ackAfter))
			}
			// Every alert was pending first; firing and resolved ones also have a start.
			pendingSince := started.Add(-time.Minute)
			var startedAt *time.Time
			if status == "pending" {
				pendingSince = started
			} else {
				startedAt = &started
			}
			var id uuid.UUID
			// next_step past the last escalation step: the seeded alerts send nothing.
			if err := tx.QueryRow(ctx, `INSERT INTO alerts (organization_id, rule_id, rule_name, rule_kind, severity, subject_type, subject_id,
				subject_name, subject_labels, status, details, pending_since, started_at, resolved_at, acknowledged_by, acknowledged_at, next_step)
				SELECT $1, r.id, r.name, r.kind, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, 99 FROM alert_rules r WHERE r.id = $2 RETURNING id`,
				s.org, s.rules[rule], severity, subjectType, subject, subjectName, labels, status, mustJSON(details),
				pendingSince, startedAt, resolved, ack, ackAt).Scan(&id); err != nil {
				return err
			}
			for _, e := range events {
				var chID, userID *uuid.UUID
				if e.channel != "" {
					c := s.channels[e.channel]
					chID = &c
				}
				if e.user != "" {
					u := s.user(e.user)
					userID = &u
				}
				detail := e.detail
				if e.kind == "fired" {
					detail = string(mustJSON(details))
				}
				if _, err := tx.Exec(ctx, "INSERT INTO alert_events (alert_id, at, kind, channel_id, channel_name, user_id, detail) VALUES ($1, $2, $3, $4, $5, $6, $7)",
					id, started.Add(e.after), e.kind, chID, e.channel, userID, detail); err != nil {
					return err
				}
			}
			return nil
		}
		fired := showcaseEvent{kind: "fired"}
		mailed := func(after time.Duration) showcaseEvent {
			return showcaseEvent{kind: "notified", after: after, channel: "On-call email"}
		}
		h := time.Hour
		dbDown := map[string]any{"error": "connection failed: host not found"}
		monLabels := map[string][]string{"Orders DB": {"prod", "db"}, "Mobile API · API ទូរស័ព្ទ": {"prod", "mobile"}, "Staging API": {"staging"}}

		// Open.
		if err := add("Production down · ផលិតកម្មដាច់", "critical", "monitor", s.monitors["Orders DB"], "Orders DB", monLabels["Orders DB"], "firing", dbDown,
			s.now.Add(-2*h+time.Minute), nil, "", 0, []showcaseEvent{fired, mailed(5 * time.Second),
				{kind: "escalated", after: 15 * time.Minute, detail: "step 2"},
				{kind: "notified", after: 15*time.Minute + 4*time.Second, channel: "Platform leads (Khmer)"}}); err != nil {
			return err
		}
		if err := add("api-2 agent offline", "critical", "asset", s.assets["api-2"], "api-2", []string{"prod", "api"}, "firing",
			map[string]any{"since": s.now.Add(-3 * h)}, s.now.Add(-3*h+5*time.Minute), nil, "admin@demo.opshub.local", 40*time.Minute,
			[]showcaseEvent{fired, mailed(3 * time.Second), {kind: "acknowledged", after: 40 * time.Minute, user: "admin@demo.opshub.local"}}); err != nil {
			return err
		}
		for _, c := range []struct {
			name    string
			details map[string]any
		}{
			{"api.mekong.example", map[string]any{"days": 8, "not_after": s.now.AddDate(0, 0, 9)}},
			{"old.mekong.example", map[string]any{"days": -3, "not_after": s.now.AddDate(0, 0, -3)}},
			{"status.mekong.invalid", map[string]any{"error": "dial tcp: lookup status.mekong.invalid: no such host"}},
		} {
			ev := []showcaseEvent{fired, {kind: "notified", after: 6 * time.Second, channel: "Platform leads (Khmer)"}}
			if c.name == "old.mekong.example" {
				ev = []showcaseEvent{fired, {kind: "silenced", after: time.Second, detail: "Domain being retired"}}
			}
			labels := []string{"prod", "api"}
			switch c.name {
			case "old.mekong.example":
				labels = []string{"legacy"}
			case "status.mekong.invalid":
				labels = []string{"web"}
			}
			if err := add("Certificates expiring", "warning", "asset", s.assets[c.name], c.name, labels, "firing", c.details,
				s.now.Add(-26*h), nil, "", 0, ev); err != nil {
				return err
			}
		}
		if err := add("Staging down", "warning", "monitor", s.monitors["Staging API"], "Staging API", monLabels["Staging API"], "pending", dbDown,
			s.now.Add(-19*time.Minute), nil, "", 0, nil); err != nil {
			return err
		}

		// Resolved over the last 30 days.
		type past struct {
			rule, severity, subjectType, subject string
			details                              map[string]any
			daysAgo                              int
			lasted                               time.Duration
			ackBy                                string
			extra                                []showcaseEvent
		}
		history := []past{
			{"Production down · ផលិតកម្មដាច់", "critical", "monitor", "Mobile API · API ទូរស័ព្ទ", map[string]any{"error": "HTTP 502"}, 2, 14 * time.Minute, ShowcaseTwoFactorEmail, nil},
			{"Production down · ផលិតកម្មដាច់", "critical", "monitor", "Orders DB", dbDown, 6, 41 * time.Minute, "admin@demo.opshub.local",
				[]showcaseEvent{{kind: "escalated", after: 15 * time.Minute, detail: "step 2"}, {kind: "notified", after: 15*time.Minute + 3*time.Second, channel: "Platform leads (Khmer)"}}},
			{"Slow responses", "warning", "monitor", "Mobile API · API ទូរស័ព្ទ", map[string]any{"value": 2310.0, "threshold": 1500.0}, 3, 22 * time.Minute, "", nil},
			{"Slow responses", "warning", "monitor", "Mobile API · API ទូរស័ព្ទ", map[string]any{"value": 1780.0, "threshold": 1500.0}, 11, 9 * time.Minute, "", nil},
			{"Server CPU high · CPU ខ្ពស់", "warning", "asset", "api-2", map[string]any{"metric": "cpu", "value": 96.4, "threshold": 85.0}, 1, 35 * time.Minute, "rithy@demo.opshub.local", nil},
			{"Server CPU high · CPU ខ្ពស់", "warning", "asset", "api-1", map[string]any{"metric": "cpu", "value": 91.0, "threshold": 85.0}, 9, 12 * time.Minute, "", nil},
			{"Disk almost full", "critical", "asset", "api-1", map[string]any{"metric": "disk", "value": 93.1, "threshold": 90.0}, 15, 3 * h, "admin@demo.opshub.local", nil},
			{"api-2 agent offline", "critical", "asset", "api-2", map[string]any{"since": s.now.AddDate(0, 0, -18)}, 18, 25 * time.Minute, "", nil},
			{"Certificates expiring", "warning", "asset", "mekong.example", map[string]any{"days": 12, "not_after": s.now.AddDate(0, 0, -8)}, 20, 6 * 24 * h, "", nil},
			{"Beta latency (chat channels)", "info", "monitor", "Mobile API · API ទូរស័ព្ទ", map[string]any{"value": 5400.0, "threshold": 5000.0}, 4, 50 * time.Minute, "",
				[]showcaseEvent{
					{kind: "notify_failed", after: 2 * time.Second, channel: "#ops-alerts", detail: "slack: 404 Not Found (invalid webhook)"},
					{kind: "notified", after: 3 * time.Second, channel: "Telegram on-call"},
					{kind: "escalated", after: 30 * time.Minute, detail: "step 2"},
					{kind: "notified", after: 30*time.Minute + 2*time.Second, channel: "Incident webhook"}}},
			{"Production down · ផលិតកម្មដាច់", "critical", "monitor", "Orders DB", dbDown, 24, 7 * time.Minute, "", nil},
			{"Slow responses", "warning", "monitor", "Mobile API · API ទូរស័ព្ទ", map[string]any{"value": 1650.0, "threshold": 1500.0}, 27, 18 * time.Minute, "", nil},
		}
		for _, p := range history {
			id, labels := s.monitors[p.subject], monLabels[p.subject]
			if p.subjectType == "asset" {
				id, labels = s.assets[p.subject], []string{"prod"}
			}
			started := s.now.AddDate(0, 0, -p.daysAgo).Add(-time.Duration(p.daysAgo%7) * h)
			ev := []showcaseEvent{fired}
			if !strings.HasPrefix(p.rule, "Beta") {
				ch := "On-call email"
				if p.rule == "Certificates expiring" {
					ch = "Platform leads (Khmer)"
				}
				ev = append(ev, showcaseEvent{kind: "notified", after: 4 * time.Second, channel: ch})
			}
			ev = append(ev, p.extra...)
			ackAfter := p.lasted / 3
			if p.ackBy != "" {
				ev = append(ev, showcaseEvent{kind: "acknowledged", after: ackAfter, user: p.ackBy})
			}
			ev = append(ev, showcaseEvent{kind: "resolved", after: p.lasted})
			if err := add(p.rule, p.severity, p.subjectType, id, p.subject, labels, "resolved", p.details, started, at(started.Add(p.lasted)),
				p.ackBy, ackAfter, ev); err != nil {
				return err
			}
		}

		admin, secure := s.user("admin@demo.opshub.local"), s.user(ShowcaseTwoFactorEmail)
		for _, si := range []struct {
			rule, subject, label, severity, comment string
			from, to                                time.Time
			by                                      uuid.UUID
		}{
			{subject: "old.mekong.example", comment: "Domain being retired · ដែនកំពុងឈប់ប្រើ", from: s.now.Add(-26 * h), to: s.now.AddDate(0, 0, 6), by: admin},
			{label: "staging", comment: "Database upgrade window · ពេលដំឡើងមូលដ្ឋានទិន្នន័យ", from: s.now.Add(50 * h), to: s.now.Add(54 * h), by: secure},
			{rule: "Server CPU high · CPU ខ្ពស់", comment: "Load test", from: s.now.AddDate(0, 0, -10), to: s.now.AddDate(0, 0, -10).Add(2 * h), by: admin},
			{severity: "info", comment: "Quiet weekend", from: s.now.AddDate(0, 0, -20), to: s.now.AddDate(0, 0, -18), by: secure},
		} {
			var rule, subject *uuid.UUID
			var label, severity *string
			if si.rule != "" {
				r := s.rules[si.rule]
				rule = &r
			}
			if si.subject != "" {
				a := s.assets[si.subject]
				subject = &a
			}
			if si.label != "" {
				label = &si.label
			}
			if si.severity != "" {
				severity = &si.severity
			}
			if _, err := tx.Exec(ctx, `INSERT INTO silences (organization_id, rule_id, subject_id, label, severity, comment, starts_at, ends_at, created_by, created_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`, s.org, rule, subject, label, severity, si.comment, si.from, si.to, si.by,
				minTime(si.from, s.now)); err != nil {
				return err
			}
		}
		return nil
	})
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// logs creates ingest tokens for three services (one never used) and sends the last six
// hours of their output at every level.
func (s *sc) logs(retentionDays int) error {
	ctx := s.ctx
	actx := authn.WithPrincipal(ctx, authn.Principal{Kind: authn.KindSession, UserID: s.user("admin@demo.opshub.local"), SessionID: uuid.New()})
	svc := logs.NewService(s.pool, logs.Config{RetentionDays: retentionDays}, slog.New(slog.DiscardHandler))
	if err := svc.Maintain(ctx); err != nil {
		return err
	}
	type line struct {
		level, message string
		attrs          map[string]any
	}
	services := []struct {
		name, service string
		lines         []line
	}{
		{"Mobile API", "mobile-app/api", []line{
			{"info", "GET /v1/products 200", map[string]any{"path": "/v1/products", "status": 200, "ms": 34}},
			{"info", "POST /v1/orders 201", map[string]any{"path": "/v1/orders", "status": 201, "ms": 212, "platform": "android"}},
			{"debug", "cache miss catalogue:km", map[string]any{"key": "catalogue:km"}},
			{"info", "push sent to 1,204 devices", map[string]any{"campaign": "pchum-ben-sale"}},
			{"warn", "slow query on orders (812 ms)", map[string]any{"table": "orders", "ms": 812}},
			{"info", "អ្នកប្រើថ្មីបានចុះឈ្មោះ", map[string]any{"platform": "ios"}},
			{"error", "orders-db: connection refused", map[string]any{"db": "orders", "retry_in_ms": 1000}},
			{"warn", "falling back to the read replica", map[string]any{"db": "orders"}},
			{"info", "GET /healthz 200", map[string]any{"path": "/healthz", "status": 200, "ms": 1}},
			{"error", "payment provider returned 500", map[string]any{"provider": "aba", "order": 88213}},
		}},
		{"Report workers", "data-pipeline/reports", []line{
			{"info", "report job started", map[string]any{"report": "daily-sales"}},
			{"debug", "loaded 31 stores", map[string]any{"stores": 31}},
			{"info", "ការលក់សរុប ៖ ៤២ លានរៀល", map[string]any{"currency": "KHR"}},
			{"warn", "store 17 sent no data today", map[string]any{"store": 17, "city": "Siem Reap"}},
			{"info", "report emailed to finance", map[string]any{"to": "finance@mekong.example"}},
		}},
	}
	accepted := 0
	for _, sv := range services {
		tok, err := svc.CreateToken(actx, s.org, logs.TokenInput{Name: sv.name, Service: sv.service})
		if err != nil {
			return err
		}
		st, err := svc.Authenticate(ctx, tok.Token)
		if err != nil {
			return err
		}
		var b strings.Builder
		const n = 90
		start := s.now.Add(-6 * time.Hour)
		for i := range n {
			l := sv.lines[i%len(sv.lines)]
			rec := map[string]any{"ts": start.Add(time.Duration(i) * 6 * time.Hour / n).Format(time.RFC3339Nano), "level": l.level, "message": l.message}
			for k, v := range l.attrs {
				rec[k] = v
			}
			raw, err := json.Marshal(rec)
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
		accepted += res.Accepted
	}
	// A token that was never used.
	if _, err := svc.CreateToken(actx, s.org, logs.TokenInput{Name: "Edge proxy (not sending yet)", Service: "infra/edge"}); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(s.out, "seed: showcase logs: 3 ingest tokens, %d lines\n", accepted)
	return nil
}
