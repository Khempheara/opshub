import { fieldErrorMessage } from '@/lib/api/errors';
import { ApiError } from '@/lib/api/fetcher';
import type {
  AlertDetails,
  AlertRule,
  AlertRuleKind,
  AlertRuleRequest,
  AlertSeverity,
  ChannelKind,
  EscalationStep,
  Monitor,
  MonitorKind,
  MonitorRequest,
  NotificationChannel,
  NotificationChannelRequest,
} from '@/lib/api/generated/model';

/** Rounds a maximum up to a readable scale (1, 2 or 5 × 10ⁿ). */
export function niceMax(v: number): number {
  if (!(v > 0)) return 1;
  const p = 10 ** Math.floor(Math.log10(v));
  return ([1, 2, 5, 10].find((m) => m * p >= v) ?? 10) * p;
}

/** Splits "a, b  c" into labels (the API lowercases, de-duplicates and validates). */
export function parseList(s: string): string[] {
  return s
    .split(/[,\s]+/)
    .map((x) => x.trim())
    .filter(Boolean);
}

/** A number field: "" → undefined, invalid → NaN (the API reports it). */
function num(s: string): number | undefined {
  const v = s.trim();
  return v === '' ? undefined : Number(v);
}

// ── Monitors ──────────────────────────────────────────────────────────────────────────

export interface MonitorForm {
  name: string;
  kind: MonitorKind;
  target: string;
  interval: string; // seconds
  timeout: string; // seconds
  method: 'GET' | 'HEAD';
  expectedStatus: string; // "200, 204"
  keyword: string;
  expiryDays: string;
  labels: string;
  enabled: boolean;
}

export const emptyMonitorForm: MonitorForm = {
  name: '',
  kind: 'http',
  target: '',
  interval: '60',
  timeout: '10',
  method: 'GET',
  expectedStatus: '',
  keyword: '',
  expiryDays: '14',
  labels: '',
  enabled: true,
};

export function monitorRequest(f: MonitorForm, create: boolean): MonitorRequest {
  const timeout = num(f.timeout);
  const req: MonitorRequest = {
    ...(create ? { kind: f.kind } : {}),
    name: f.name.trim(),
    target: f.target.trim(),
    interval_seconds: num(f.interval),
    timeout_ms: timeout === undefined ? undefined : Math.round(timeout * 1000),
    labels: parseList(f.labels),
    enabled: f.enabled,
    settings: {},
  };
  if (f.kind === 'http') {
    req.settings = {
      method: f.method,
      expected_status: parseList(f.expectedStatus).map(Number),
      ...(f.keyword ? { keyword: f.keyword } : {}),
    };
  } else if (f.kind === 'ssl') {
    req.settings = { expiry_days: num(f.expiryDays) };
  }
  return req;
}

export function monitorForm(m: Monitor): MonitorForm {
  return {
    name: m.name,
    kind: m.kind,
    target: m.target,
    interval: String(m.interval_seconds),
    timeout: String(m.timeout_ms / 1000),
    method: m.settings.method ?? 'GET',
    expectedStatus: (m.settings.expected_status ?? []).join(', '),
    keyword: m.settings.keyword ?? '',
    expiryDays: String(m.settings.expiry_days ?? 14),
    labels: m.labels.join(', '),
    enabled: m.enabled,
  };
}

// ── Alert rules ───────────────────────────────────────────────────────────────────────

/** Rule kinds and what they watch. */
export const RULE_KINDS: AlertRuleKind[] = ['monitor_down', 'monitor_latency', 'asset_metric', 'asset_offline', 'certificate'];
export const watchesMonitors = (k: AlertRuleKind) => k === 'monitor_down' || k === 'monitor_latency';
export const needsThreshold = (k: AlertRuleKind) => k === 'monitor_latency' || k === 'asset_metric' || k === 'certificate';

export interface StepForm {
  afterMinutes: string;
  channelIds: string[];
}

export interface RuleForm {
  name: string;
  kind: AlertRuleKind;
  targetId: string; // "" = all
  label: string;
  threshold: string;
  metric: 'cpu' | 'mem' | 'disk';
  forMinutes: string;
  severity: AlertSeverity;
  steps: StepForm[];
  enabled: boolean;
}

export const defaultThreshold: Record<AlertRuleKind, string> = {
  monitor_down: '',
  monitor_latency: '1000',
  asset_metric: '90',
  asset_offline: '',
  certificate: '14',
};

export function emptyRuleForm(kind: AlertRuleKind = 'monitor_down'): RuleForm {
  return {
    name: '',
    kind,
    targetId: '',
    label: '',
    threshold: defaultThreshold[kind],
    metric: 'cpu',
    forMinutes: kind === 'monitor_down' ? '1' : '5',
    severity: 'warning',
    steps: [{ afterMinutes: '0', channelIds: [] }],
    enabled: true,
  };
}

export function ruleRequest(f: RuleForm, create: boolean): AlertRuleRequest {
  const minutes = num(f.forMinutes);
  return {
    ...(create ? { kind: f.kind } : {}),
    name: f.name.trim(),
    target_id: f.targetId || null,
    label: f.label.trim() || null,
    threshold: needsThreshold(f.kind) ? (num(f.threshold) ?? null) : null,
    metric: f.kind === 'asset_metric' ? f.metric : null,
    for_seconds: minutes === undefined ? 0 : Math.round(minutes * 60),
    severity: f.severity,
    // Steps without channels are dropped: a rule may notify nobody (alerts still show).
    escalation: f.steps
      .filter((s) => s.channelIds.length > 0)
      .map((s): EscalationStep => ({ after_minutes: num(s.afterMinutes) ?? 0, channel_ids: s.channelIds })),
    enabled: f.enabled,
  };
}

export function ruleForm(r: AlertRule): RuleForm {
  return {
    name: r.name,
    kind: r.kind,
    targetId: r.target_id ?? '',
    label: r.label ?? '',
    threshold: r.threshold === null ? defaultThreshold[r.kind] : String(r.threshold),
    metric: r.metric ?? 'cpu',
    forMinutes: String(r.for_seconds / 60),
    severity: r.severity,
    steps: r.escalation.length > 0 ? r.escalation.map((s) => ({ afterMinutes: String(s.after_minutes), channelIds: s.channel_ids })) : [{ afterMinutes: '0', channelIds: [] }],
    enabled: r.enabled,
  };
}

// ── Channels ──────────────────────────────────────────────────────────────────────────

export interface ChannelForm {
  name: string;
  kind: ChannelKind;
  chatId: string;
  botToken: string;
  webhookUrl: string;
  addresses: string;
  url: string;
  signingSecret: string;
  locale: '' | 'en' | 'km';
}

export const emptyChannelForm: ChannelForm = {
  name: '',
  kind: 'telegram',
  chatId: '',
  botToken: '',
  webhookUrl: '',
  addresses: '',
  url: '',
  signingSecret: '',
  locale: '',
};

/** Empty secrets are omitted, so an update keeps the stored ones. */
export function channelRequest(f: ChannelForm, create: boolean): NotificationChannelRequest {
  const req: NotificationChannelRequest = {
    ...(create ? { kind: f.kind } : {}),
    name: f.name.trim(),
    locale: f.locale || null,
    config: {},
    secrets: {},
  };
  switch (f.kind) {
    case 'telegram':
      req.config = { chat_id: f.chatId.trim() };
      if (f.botToken.trim()) req.secrets = { bot_token: f.botToken.trim() };
      break;
    case 'slack':
      if (f.webhookUrl.trim()) req.secrets = { webhook_url: f.webhookUrl.trim() };
      break;
    case 'email':
      req.config = { addresses: parseList(f.addresses) };
      break;
    case 'webhook':
      req.config = { url: f.url.trim() };
      if (f.signingSecret) req.secrets = { signing_secret: f.signingSecret };
      break;
  }
  return req;
}

export function channelForm(c: NotificationChannel): ChannelForm {
  return {
    ...emptyChannelForm,
    name: c.name,
    kind: c.kind,
    chatId: c.config.chat_id ?? '',
    addresses: (c.config.addresses ?? []).join(', '),
    url: c.config.url ?? '',
    locale: c.locale ?? '',
  };
}

// ── Alert text ────────────────────────────────────────────────────────────────────────

export type SummaryKey =
  | 'summary.monitor_down'
  | 'summary.monitor_down_noerror'
  | 'summary.monitor_latency'
  | 'summary.asset_metric_cpu'
  | 'summary.asset_metric_mem'
  | 'summary.asset_metric_disk'
  | 'summary.asset_offline'
  | 'summary.certificate_expiring'
  | 'summary.certificate_expired'
  | 'summary.certificate_failed';

/** The i18n key (monitoring namespace) and values that describe what an alert observed. */
export function alertSummary(kind: AlertRuleKind, d: AlertDetails, subject: string): { key: SummaryKey; values: Record<string, unknown> } {
  const values: Record<string, unknown> = { subject, value: d.value, threshold: d.threshold, error: d.error };
  switch (kind) {
    case 'monitor_down':
      return { key: d.error ? 'summary.monitor_down' : 'summary.monitor_down_noerror', values };
    case 'monitor_latency':
      return { key: 'summary.monitor_latency', values };
    case 'asset_metric': {
      const metric = d.metric === 'mem' || d.metric === 'disk' ? d.metric : 'cpu';
      return { key: `summary.asset_metric_${metric}`, values };
    }
    case 'asset_offline':
      return { key: 'summary.asset_offline', values: { ...values, since: d.since } };
    case 'certificate':
      if (d.error) return { key: 'summary.certificate_failed', values };
      // A negative number of days: it has expired already.
      if ((d.days ?? 0) < 0) return { key: 'summary.certificate_expired', values: { ...values, count: -(d.days ?? 0) } };
      return { key: 'summary.certificate_expiring', values: { ...values, count: d.days ?? 0 } };
  }
}

// ── Errors ────────────────────────────────────────────────────────────────────────────

/** The translated error of a field (or of its children, e.g. "escalation[0]…"). */
export function fieldError(error: unknown, name: string): string | undefined {
  if (!(error instanceof ApiError)) return undefined;
  const fe = error.fieldErrors.find((f) => f.field === name || f.field.startsWith(`${name}.`) || f.field.startsWith(`${name}[`));
  return fe ? fieldErrorMessage(fe) : undefined;
}

/** Whether an error has field errors outside the given fields (so a general message is needed). */
export function hasOtherErrors(error: unknown, fields: string[]): boolean {
  if (error === null || error === undefined) return false;
  if (!(error instanceof ApiError) || error.fieldErrors.length === 0) return true;
  return !error.fieldErrors.every((f) => fields.some((n) => f.field === n || f.field.startsWith(`${n}.`) || f.field.startsWith(`${n}[`)));
}
