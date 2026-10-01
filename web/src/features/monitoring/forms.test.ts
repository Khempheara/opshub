import { describe, expect, it } from 'vitest';
import type { AlertRule, Monitor, NotificationChannel } from '@/lib/api/generated/model';
import {
  alertSummary,
  channelForm,
  channelRequest,
  emptyChannelForm,
  emptyMonitorForm,
  emptyRuleForm,
  monitorForm,
  monitorRequest,
  niceMax,
  parseList,
  ruleForm,
  ruleRequest,
} from './forms';

describe('niceMax', () => {
  it('rounds up to 1, 2 or 5 × 10ⁿ', () => {
    expect(niceMax(0)).toBe(1);
    expect(niceMax(87)).toBe(100);
    expect(niceMax(120)).toBe(200);
    expect(niceMax(430)).toBe(500);
    expect(niceMax(1000)).toBe(1000);
    expect(niceMax(Number.NaN)).toBe(1);
  });
});

describe('monitor forms', () => {
  it('builds an http request with its settings', () => {
    const r = monitorRequest({ ...emptyMonitorForm, name: ' api ', target: ' https://api.example.com ', expectedStatus: '200, 204', keyword: 'ok', timeout: '2.5', labels: 'prod web' }, true);
    expect(r).toEqual({
      kind: 'http',
      name: 'api',
      target: 'https://api.example.com',
      interval_seconds: 60,
      timeout_ms: 2500,
      labels: ['prod', 'web'],
      enabled: true,
      settings: { method: 'GET', expected_status: [200, 204], keyword: 'ok' },
    });
  });
  it('sends only the settings of the kind, and no kind on update', () => {
    expect(monitorRequest({ ...emptyMonitorForm, kind: 'ssl', expiryDays: '30' }, false)).toMatchObject({ settings: { expiry_days: 30 } });
    expect(monitorRequest({ ...emptyMonitorForm, kind: 'tcp' }, false)).not.toHaveProperty('kind');
    expect(monitorRequest({ ...emptyMonitorForm, kind: 'tcp', interval: '' }, true).interval_seconds).toBeUndefined();
  });
  it('round-trips a monitor', () => {
    const m = {
      name: 'shop', kind: 'ssl', target: 'shop.example.com:443', interval_seconds: 300, timeout_ms: 5000,
      settings: { expiry_days: 21 }, labels: ['prod'], enabled: false,
    } as Monitor;
    expect(monitorForm(m)).toMatchObject({ interval: '300', timeout: '5', expiryDays: '21', labels: 'prod', enabled: false });
  });
});

describe('rule forms', () => {
  it('converts minutes, drops empty steps and threshold-less kinds', () => {
    const f = { ...emptyRuleForm('monitor_down'), name: 'Down', forMinutes: '2', threshold: '99', steps: [
      { afterMinutes: '0', channelIds: ['a'] },
      { afterMinutes: '15', channelIds: [] },
    ] };
    expect(ruleRequest(f, true)).toEqual({
      kind: 'monitor_down', name: 'Down', target_id: null, label: null, threshold: null, metric: null, for_seconds: 120,
      severity: 'warning', escalation: [{ after_minutes: 0, channel_ids: ['a'] }], enabled: true,
    });
  });
  it('keeps metric and threshold for metric rules', () => {
    expect(ruleRequest({ ...emptyRuleForm('asset_metric'), metric: 'disk' }, false)).toMatchObject({ threshold: 90, metric: 'disk' });
    expect(ruleRequest({ ...emptyRuleForm('certificate') }, false)).toMatchObject({ threshold: 14, metric: null });
  });
  it('round-trips a rule', () => {
    const r = { name: 'CPU', kind: 'asset_metric', target_id: null, label: 'prod', threshold: 85, metric: 'cpu', for_seconds: 300,
      severity: 'critical', escalation: [{ after_minutes: 0, channel_ids: ['c'] }], enabled: true } as unknown as AlertRule;
    expect(ruleForm(r)).toMatchObject({ threshold: '85', forMinutes: '5', label: 'prod', steps: [{ afterMinutes: '0', channelIds: ['c'] }] });
  });
});

describe('channel forms', () => {
  it('omits empty secrets so updates keep them', () => {
    expect(channelRequest({ ...emptyChannelForm, name: 'tg', chatId: ' -100 ' }, false)).toEqual({ name: 'tg', locale: null, config: { chat_id: '-100' }, secrets: {} });
    expect(channelRequest({ ...emptyChannelForm, kind: 'webhook', name: 'w', url: 'https://x', signingSecret: 's3cr3t', locale: 'km' }, true)).toEqual({
      kind: 'webhook', name: 'w', locale: 'km', config: { url: 'https://x' }, secrets: { signing_secret: 's3cr3t' },
    });
    expect(channelRequest({ ...emptyChannelForm, kind: 'email', name: 'm', addresses: 'a@x.com, b@x.com' }, true).config).toEqual({ addresses: ['a@x.com', 'b@x.com'] });
  });
  it('never puts secrets back into the form', () => {
    const c = { name: 'slack', kind: 'slack', config: {}, secrets: ['webhook_url'], locale: null } as unknown as NotificationChannel;
    expect(channelForm(c).webhookUrl).toBe('');
  });
});

describe('alertSummary', () => {
  it('picks the message for the kind', () => {
    expect(alertSummary('monitor_down', { error: 'timed out' }, 'web').key).toBe('summary.monitor_down');
    expect(alertSummary('monitor_down', {}, 'web').key).toBe('summary.monitor_down_noerror');
    expect(alertSummary('asset_metric', { metric: 'mem', value: 93 }, 'web-1').key).toBe('summary.asset_metric_mem');
    expect(alertSummary('certificate', { days: 5 }, 'shop').values.count).toBe(5);
    expect(alertSummary('certificate', { error: 'x' }, 'shop').key).toBe('summary.certificate_failed');
    expect(alertSummary('certificate', { days: -3 }, 'shop')).toMatchObject({ key: 'summary.certificate_expired', values: { count: 3 } });
  });
  it('splits lists', () => {
    expect(parseList(' a, b  c,,')).toEqual(['a', 'b', 'c']);
  });
});
