package alert

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/opshub/opshub/internal/apperr"
	"github.com/opshub/opshub/internal/jobs"
	"github.com/opshub/opshub/internal/notify"
	"github.com/opshub/opshub/internal/store"
)

func TestRuleValidation(t *testing.T) {
	e := newEnv(t)
	mon := e.monitor(t, "web", nil, 0)
	srv := e.server(t, "web-1", time.Second, 10)
	dom := e.domain(t, "shop.example.com", 60*24*time.Hour, "")
	ch := e.channel(t, "ops")
	other := newEnv(t).channel(t, "theirs")
	for _, c := range []struct {
		in   RuleInput
		want []string
	}{
		{RuleInput{Kind: store.AlertRuleKindMonitorDown, Severity: "urgent", ForSeconds: -1, Label: ptr("Bad Label")},
			[]string{"name:range", "severity:oneof", "for_seconds:range", "label:pattern"}},
		{RuleInput{Name: "x", Kind: "cpu"}, []string{"kind:oneof"}},
		{RuleInput{Name: "x", Kind: store.AlertRuleKindMonitorLatency}, []string{"threshold:required"}},
		{RuleInput{Name: "x", Kind: store.AlertRuleKindAssetMetric, Threshold: ptr(150.0), Metric: ptr("gpu")},
			[]string{"threshold:range", "metric:oneof"}},
		{RuleInput{Name: "x", Kind: store.AlertRuleKindMonitorDown, TargetID: &srv}, []string{"target_id:exists"}},
		{RuleInput{Name: "x", Kind: store.AlertRuleKindAssetOffline, TargetID: &dom}, []string{"target_id:exists"}},
		{RuleInput{Name: "x", Kind: store.AlertRuleKindCertificate, Threshold: ptr(14.0), TargetID: &srv}, []string{"target_id:exists"}},
		{RuleInput{Name: "x", Kind: store.AlertRuleKindMonitorDown, Escalation: []Step{
			{AfterMinutes: 5, ChannelIDs: []uuid.UUID{ch}},
			{AfterMinutes: 3, ChannelIDs: []uuid.UUID{other}},
			{AfterMinutes: 10},
		}}, []string{"escalation[0].after_minutes:first_step_immediate", "escalation[1].after_minutes:increasing",
			"escalation[1].channel_ids[0]:exists", "escalation[2].channel_ids:range"}},
	} {
		_, err := e.svc.CreateRule(e.dev.ctx, e.orgID, c.in)
		assert.ElementsMatch(t, c.want, fieldsOf(t, err), "%+v", c.in)
	}
	r := e.rule(t, RuleInput{Name: "Down", Kind: store.AlertRuleKindMonitorDown, TargetID: &mon, Threshold: ptr(5.0), Metric: ptr("cpu"),
		Escalation: []Step{{ChannelIDs: []uuid.UUID{ch}}}})
	assert.Nil(t, r.Threshold, "ignored for this kind")
	assert.Nil(t, r.Metric)
	assert.Equal(t, store.AlertSeverityWarning, r.Severity)
	assert.True(t, r.Enabled)
	_, err := e.svc.CreateRule(e.dev.ctx, e.orgID, RuleInput{Name: "Down", Kind: store.AlertRuleKindMonitorDown})
	assert.Equal(t, apperr.CodeRuleNameTaken, codeOf(t, err))
}

func TestRulePermissions(t *testing.T) {
	e := newEnv(t)
	in := RuleInput{Name: "Down", Kind: store.AlertRuleKindMonitorDown}
	_, err := e.svc.CreateRule(e.viewer.ctx, e.orgID, in)
	assert.Equal(t, apperr.CodeForbidden, codeOf(t, err))
	r := e.rule(t, in)
	list, err := e.svc.ListRules(e.viewer.ctx, e.orgID)
	require.NoError(t, err)
	assert.Len(t, list, 1)
	_, err = e.svc.GetRule(e.outsider.ctx, r.ID)
	assert.Equal(t, apperr.CodeRuleNotFound, codeOf(t, err))
	_, err = e.svc.UpdateRule(e.dev.ctx, r.ID, r.Version+1, in)
	assert.Equal(t, apperr.CodeVersionConflict, codeOf(t, err))
	_, err = e.svc.ListAlerts(e.outsider.ctx, e.orgID, AlertFilter{})
	assert.Equal(t, apperr.CodeOrgNotFound, codeOf(t, err))
	assert.Equal(t, apperr.CodeForbidden, codeOf(t, e.svc.DeleteRule(e.viewer.ctx, r.ID)))
	require.NoError(t, e.svc.DeleteRule(e.dev.ctx, r.ID))
}

func TestMonitorDownFiresEscalatesAndResolves(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	web := e.monitor(t, "web", ptr(true), 120, "prod")
	e.monitor(t, "staging", ptr(false), 0, "staging") // down, but not labelled prod
	first, second := e.channel(t, "chat"), e.channel(t, "pager")
	r := e.rule(t, RuleInput{Name: "Prod down", Kind: store.AlertRuleKindMonitorDown, Label: ptr("prod"), ForSeconds: 60,
		Severity: store.AlertSeverityCritical, Escalation: []Step{
			{ChannelIDs: []uuid.UUID{first}},
			{AfterMinutes: 10, ChannelIDs: []uuid.UUID{second}},
		}})

	e.tick(t)
	assert.Empty(t, e.open(t, r.ID), "everything labelled prod is up")

	// Down: pending until it has been down for 60 s.
	e.setMonitor(t, web, false, 0)
	e.tick(t)
	a := e.open(t, r.ID)[web]
	assert.Equal(t, store.AlertStatusPending, a.Status)
	assert.Empty(t, e.jobs.take())
	list, err := e.svc.ListAlerts(e.viewer.ctx, e.orgID, AlertFilter{})
	require.NoError(t, err)
	assert.Empty(t, list, "pending alerts aren't listed")
	_, err = e.svc.GetAlert(e.viewer.ctx, a.ID)
	assert.Equal(t, apperr.CodeAlertNotFound, codeOf(t, err))

	e.shift = 61 * time.Second
	e.tick(t)
	a = e.open(t, r.ID)[web]
	assert.Equal(t, store.AlertStatusFiring, a.Status)
	assert.Equal(t, []jobs.NotifyArgs{{AlertID: a.ID, ChannelID: first, Event: notify.EventFiring, Step: 0}}, e.jobs.take())
	e.delivered(t, a.ID, first)

	// The next step is due 10 minutes after it started.
	e.tick(t)
	assert.Empty(t, e.jobs.take())
	e.exec(t, `UPDATE alerts SET next_step_at = now() - interval '1 second' WHERE id = $1`, a.ID)
	e.tick(t)
	assert.Equal(t, []jobs.NotifyArgs{{AlertID: a.ID, ChannelID: second, Event: notify.EventFiring, Step: 1}}, e.jobs.take())
	e.delivered(t, a.ID, second)

	list, err = e.svc.ListAlerts(e.viewer.ctx, e.orgID, AlertFilter{Status: ptr(store.AlertStatusFiring)})
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "web", list[0].SubjectName)
	assert.Equal(t, "unexpected status 503", list[0].Details.Error)
	assert.Equal(t, store.AlertSeverityCritical, list[0].Severity)

	// Back up: resolved, and both channels hear about it.
	e.setMonitor(t, web, true, 90)
	e.tick(t)
	assert.Empty(t, e.open(t, r.ID))
	assert.ElementsMatch(t, []jobs.NotifyArgs{
		{AlertID: a.ID, ChannelID: first, Event: notify.EventResolved},
		{AlertID: a.ID, ChannelID: second, Event: notify.EventResolved},
	}, e.jobs.take())
	d, err := e.svc.GetAlert(e.viewer.ctx, a.ID)
	require.NoError(t, err)
	assert.Equal(t, store.AlertStatusResolved, d.Status)
	require.NotNil(t, d.ResolvedAt)
	assert.Equal(t, []string{"fired", "notified", "escalated", "notified", "resolved"}, timeline(t, e, a.ID))

	// A condition that clears while pending leaves nothing behind.
	e.shift = 0
	e.setMonitor(t, web, false, 0)
	e.tick(t)
	require.Len(t, e.open(t, r.ID), 1)
	e.setMonitor(t, web, true, 90)
	e.tick(t)
	assert.Empty(t, e.open(t, r.ID))
	var n int
	require.NoError(t, e.svc.pool.QueryRow(ctx, `SELECT count(*) FROM alerts WHERE rule_id = $1`, r.ID).Scan(&n))
	assert.Equal(t, 1, n, "only the resolved alert remains")
}

func TestAcknowledgeStopsEscalation(t *testing.T) {
	e := newEnv(t)
	web := e.monitor(t, "web", ptr(false), 0)
	first, second := e.channel(t, "chat"), e.channel(t, "pager")
	r := e.rule(t, RuleInput{Name: "Down", Kind: store.AlertRuleKindMonitorDown, Escalation: []Step{
		{ChannelIDs: []uuid.UUID{first}}, {AfterMinutes: 5, ChannelIDs: []uuid.UUID{second}},
	}})
	e.tick(t)
	a := e.open(t, r.ID)[web]
	require.Equal(t, store.AlertStatusFiring, a.Status, "for_seconds 0 fires at once")
	assert.Len(t, e.jobs.take(), 1)

	_, err := e.svc.Acknowledge(e.viewer.ctx, a.ID)
	assert.Equal(t, apperr.CodeForbidden, codeOf(t, err), "acknowledging needs alert.ack (Developer)")
	d, err := e.svc.Acknowledge(e.dev.ctx, a.ID)
	require.NoError(t, err)
	require.NotNil(t, d.AcknowledgedAt)
	assert.NotNil(t, d.AcknowledgedByName)
	_, err = e.svc.Acknowledge(e.dev.ctx, a.ID)
	assert.Equal(t, apperr.CodeAlertNotFiring, codeOf(t, err))

	e.exec(t, `UPDATE alerts SET next_step_at = now() - interval '1 minute' WHERE id = $1`, a.ID)
	e.tick(t)
	assert.Empty(t, e.jobs.take(), "no more steps after an acknowledgement")
	assert.Equal(t, store.AlertStatusFiring, e.open(t, r.ID)[web].Status, "still firing until it recovers")
	assert.Equal(t, []string{"fired", "acknowledged"}, timeline(t, e, a.ID))
}

func TestSilences(t *testing.T) {
	e := newEnv(t)
	web := e.monitor(t, "web", ptr(false), 0, "prod")
	ch := e.channel(t, "chat")
	r := e.rule(t, RuleInput{Name: "Down", Kind: store.AlertRuleKindMonitorDown, Escalation: []Step{{ChannelIDs: []uuid.UUID{ch}}}})

	for _, c := range []struct {
		in   SilenceInput
		want []string
	}{
		{SilenceInput{EndsAt: time.Now().Add(time.Hour)}, []string{"matchers:required"}},
		{SilenceInput{Label: ptr("prod"), EndsAt: time.Now().Add(-time.Minute)}, []string{"ends_at:after"}},
		{SilenceInput{Label: ptr("prod"), EndsAt: time.Now().Add(100 * 24 * time.Hour)}, []string{"ends_at:max_range"}},
		{SilenceInput{RuleID: ptr(uuid.New()), SubjectID: ptr(uuid.New()), Severity: ptr(store.AlertSeverity("loud")), EndsAt: time.Now().Add(time.Hour)},
			[]string{"rule_id:exists", "subject_id:exists", "severity:oneof"}},
	} {
		_, err := e.svc.CreateSilence(e.dev.ctx, e.orgID, c.in)
		assert.ElementsMatch(t, c.want, fieldsOf(t, err))
	}
	_, err := e.svc.CreateSilence(e.viewer.ctx, e.orgID, SilenceInput{Label: ptr("prod"), EndsAt: time.Now().Add(time.Hour)})
	assert.Equal(t, apperr.CodeForbidden, codeOf(t, err))

	// A silence on another label doesn't match; one on this monitor's label does.
	_, err = e.svc.CreateSilence(e.dev.ctx, e.orgID, SilenceInput{Label: ptr("staging"), EndsAt: time.Now().Add(time.Hour)})
	require.NoError(t, err)
	sl, err := e.svc.CreateSilence(e.dev.ctx, e.orgID, SilenceInput{Label: ptr("PROD"), RuleID: &r.ID, Comment: "maintenance", EndsAt: time.Now().Add(time.Hour)})
	require.NoError(t, err)
	assert.Equal(t, "prod", *sl.Label)
	assert.True(t, sl.Active)

	e.tick(t)
	a := e.open(t, r.ID)[web]
	assert.Equal(t, store.AlertStatusFiring, a.Status, "silenced alerts still fire and show")
	assert.Empty(t, e.jobs.take(), "but notify nobody")
	e.exec(t, `UPDATE alerts SET next_step_at = now() - interval '1 second' WHERE id = $1`, a.ID)
	e.tick(t)
	assert.Equal(t, []string{"fired", "silenced"}, timeline(t, e, a.ID), "recorded once")
	list, err := e.svc.ListAlerts(e.viewer.ctx, e.orgID, AlertFilter{})
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.True(t, list[0].Silenced)

	// Ending the silence: the notification goes out on the next pass.
	require.NoError(t, e.svc.ExpireSilence(e.dev.ctx, sl.ID))
	assert.Equal(t, apperr.CodeSilenceNotFound, codeOf(t, e.svc.ExpireSilence(e.dev.ctx, sl.ID)))
	e.exec(t, `UPDATE alerts SET next_step_at = now() - interval '1 second' WHERE id = $1`, a.ID)
	e.tick(t)
	assert.Equal(t, []jobs.NotifyArgs{{AlertID: a.ID, ChannelID: ch, Event: notify.EventFiring}}, e.jobs.take())
	active, err := e.svc.ListSilences(e.viewer.ctx, e.orgID, false)
	require.NoError(t, err)
	assert.Len(t, active, 1, "only the staging silence is left")
	all, err := e.svc.ListSilences(e.viewer.ctx, e.orgID, true)
	require.NoError(t, err)
	assert.Len(t, all, 2)
}

func TestInfrastructureRules(t *testing.T) {
	e := newEnv(t)
	hot := e.server(t, "hot", 5*time.Second, 95, "prod")
	cool := e.server(t, "cool", 5*time.Second, 20, "prod")
	stale := e.server(t, "stale", 10*time.Minute, 99, "prod") // high, but its numbers are old
	e.exec(t, `INSERT INTO infra_assets (organization_id, kind, name, agent_token_hash, agent_token_prefix)
		VALUES ($1, 'server', 'waiting', 'hash-waiting', 'w')`, e.orgID) // never reported
	expiring := e.domain(t, "soon.example.com", 5*24*time.Hour, "")
	e.domain(t, "fine.example.com", 90*24*time.Hour, "")
	broken := e.domain(t, "broken.example.com", 90*24*time.Hour, "certificate is not valid for this name")

	cpu := e.rule(t, RuleInput{Name: "CPU", Kind: store.AlertRuleKindAssetMetric, Metric: ptr("cpu"), Threshold: ptr(90.0), Label: ptr("prod")})
	offline := e.rule(t, RuleInput{Name: "Offline", Kind: store.AlertRuleKindAssetOffline})
	certs := e.rule(t, RuleInput{Name: "Certs", Kind: store.AlertRuleKindCertificate, Threshold: ptr(14.0)})
	e.tick(t)

	got := e.open(t, cpu.ID)
	require.Len(t, got, 1)
	a := got[hot]
	var d notify.Details
	d = e.details(t, a)
	assert.InDelta(t, 95, *d.Value, 0.01)
	assert.Equal(t, "cpu", d.Metric)
	assert.NotContains(t, got, cool)
	assert.NotContains(t, got, stale)

	got = e.open(t, offline.ID)
	require.Len(t, got, 1, "the stale agent; one that never reported is waiting, not offline")
	d = e.details(t, got[stale])
	require.NotNil(t, d.Since)

	got = e.open(t, certs.ID)
	require.Len(t, got, 2)
	d = e.details(t, got[expiring])
	assert.Equal(t, 4, *d.Days)
	d = e.details(t, got[broken])
	assert.Equal(t, "certificate is not valid for this name", d.Error)

	// Cooling down resolves; targeting one server only watches it.
	e.exec(t, `UPDATE infra_assets SET last_metrics = '{"cpu_pct": 30}' WHERE id = $1`, hot)
	e.tick(t)
	assert.Empty(t, e.open(t, cpu.ID))
	only := e.rule(t, RuleInput{Name: "Cool CPU", Kind: store.AlertRuleKindAssetMetric, Metric: ptr("mem"), Threshold: ptr(10.0), TargetID: &cool})
	e.tick(t)
	assert.Len(t, e.open(t, only.ID), 1)
}

func TestLatencyAndDisableDelete(t *testing.T) {
	e := newEnv(t)
	slow := e.monitor(t, "slow", ptr(true), 2500)
	e.monitor(t, "fast", ptr(true), 80)
	e.monitor(t, "paused", ptr(false), 0)
	e.exec(t, `UPDATE monitors SET enabled = false WHERE name = 'paused' AND organization_id = $1`, e.orgID)
	ch := e.channel(t, "chat")
	lat := e.rule(t, RuleInput{Name: "Slow", Kind: store.AlertRuleKindMonitorLatency, Threshold: ptr(1000.0), Escalation: []Step{{ChannelIDs: []uuid.UUID{ch}}}})
	down := e.rule(t, RuleInput{Name: "Down", Kind: store.AlertRuleKindMonitorDown})
	e.tick(t)
	got := e.open(t, lat.ID)
	require.Len(t, got, 1)
	a := got[slow]
	assert.InDelta(t, 2500, *e.details(t, a).Value, 0)
	assert.Empty(t, e.open(t, down.ID), "paused monitors don't alert")
	e.jobs.take()
	e.delivered(t, a.ID, ch)

	// Disabling a rule resolves its alerts and tells the channels.
	r, err := e.svc.GetRule(e.viewer.ctx, lat.ID)
	require.NoError(t, err)
	in := RuleInput{Name: r.Name, Threshold: r.Threshold, Escalation: r.Escalation, Enabled: ptr(false)}
	_, err = e.svc.UpdateRule(e.dev.ctx, r.ID, r.Version, in)
	require.NoError(t, err)
	assert.Empty(t, e.open(t, lat.ID))
	assert.Equal(t, []jobs.NotifyArgs{{AlertID: a.ID, ChannelID: ch, Event: notify.EventResolved}}, e.jobs.take())
	e.tick(t)
	assert.Empty(t, e.open(t, lat.ID), "disabled rules aren't evaluated")

	// Deleting a rule keeps its alert history.
	require.NoError(t, e.svc.DeleteRule(e.dev.ctx, lat.ID))
	d, err := e.svc.GetAlert(e.viewer.ctx, a.ID)
	require.NoError(t, err)
	assert.Nil(t, d.RuleID)
	assert.Equal(t, "Slow", d.RuleName)
	assert.Equal(t, []string{"fired", "notified", "resolved"}, timeline(t, e, a.ID))
}

func (e *env) details(t *testing.T, a store.Alert) notify.Details {
	t.Helper()
	d, err := e.svc.GetAlert(e.viewer.ctx, a.ID)
	if err == nil {
		return d.Details
	}
	// Pending alerts aren't visible through the API; read the row.
	out := toAlert(a, nil, nil)
	return out.Details
}
