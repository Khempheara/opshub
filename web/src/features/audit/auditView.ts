import type { ListAuditLogParams } from '@/lib/api/generated/model';

/** Filter groups shown in the UI, each covering one or more action areas (the part before the dot). */
export const CATEGORIES = {
  organization: ['org', 'member', 'team'],
  projects: ['project', 'environment', 'repo'],
  pipelines: ['pipeline', 'run', 'job', 'approval'],
  runners: ['runner'],
  deployments: ['deploy_target', 'deployment'],
  infrastructure: ['asset'],
  secrets: ['secret'],
  monitoring: ['monitor', 'alert_rule', 'alert', 'silence', 'channel'],
  logs: ['log_token'],
  audit: ['audit'],
} as const satisfies Record<string, readonly string[]>;
export type Category = keyof typeof CATEGORIES;
export const CATEGORY_KEYS = Object.keys(CATEGORIES) as Category[];

/** The category of an action area, for its icon and label. */
export function categoryOf(area: string): Category | undefined {
  return CATEGORY_KEYS.find((c) => (CATEGORIES[c] as readonly string[]).includes(area));
}

export const PERIODS = ['24h', '7d', '30d', '90d', 'all'] as const;
export type Period = (typeof PERIODS)[number];
const PERIOD_MS: Record<Exclude<Period, 'all'>, number> = {
  '24h': 24 * 3600_000,
  '7d': 7 * 24 * 3600_000,
  '30d': 30 * 24 * 3600_000,
  '90d': 90 * 24 * 3600_000,
};

export interface AuditFilters {
  category: Category | '';
  actor: string;
  project: string;
  period: Period;
}

export const emptyAuditFilters: AuditFilters = { category: '', actor: '', project: '', period: '30d' };

/** Query parameters for the list and the export, with the period counted back from `anchor` (ms). */
export function auditParams(f: AuditFilters, anchor: number, locale: string): ListAuditLogParams {
  const p: ListAuditLogParams = { locale: locale === 'km' ? 'km' : 'en' };
  if (f.category) p.area = [...CATEGORIES[f.category]];
  if (f.actor) p.actor = f.actor;
  if (f.project) p.project = f.project;
  if (f.period !== 'all') p.from = new Date(anchor - PERIOD_MS[f.period]).toISOString();
  return p;
}

/** audit-log-<slug>-<yyyymmdd>.csv, matching the server's name. */
export function exportFileName(slug: string, now: Date): string {
  const d = now.toISOString().slice(0, 10).replaceAll('-', '');
  return `audit-log-${slug}-${d}.csv`;
}

export interface Change {
  key: string;
  before?: string;
  after?: string;
}

function show(v: unknown): string {
  return typeof v === 'string' ? v : JSON.stringify(v);
}

/**
 * What changed between before and after, key by key (sorted). A created resource lists only
 * after-values, a deleted one only before-values; unchanged keys are left out.
 */
export function changes(before: Record<string, unknown> | null, after: Record<string, unknown> | null): Change[] {
  const keys = new Set([...Object.keys(before ?? {}), ...Object.keys(after ?? {})]);
  const out: Change[] = [];
  for (const key of [...keys].sort()) {
    const b = before && key in before ? before[key] : undefined;
    const a = after && key in after ? after[key] : undefined;
    if (before && after && JSON.stringify(a) === JSON.stringify(b)) continue;
    out.push({ key, ...(b !== undefined ? { before: show(b) } : {}), ...(a !== undefined ? { after: show(a) } : {}) });
  }
  return out;
}

/** Metadata as sorted key/value text, without the project id (shown as the project). */
export function metadataEntries(meta: Record<string, unknown>): [string, string][] {
  return Object.keys(meta)
    .filter((k) => k !== 'project_id')
    .sort()
    .map((k) => [k, show(meta[k])]);
}

const BROWSERS: [RegExp, string][] = [
  [/Edg\//, 'Edge'],
  [/OPR\//, 'Opera'],
  [/Firefox\//, 'Firefox'],
  [/Chrome\//, 'Chrome'],
  [/Safari\//, 'Safari'],
  [/curl\//, 'curl'],
  [/opshub-runner/, 'opshub-runner'],
];
const SYSTEMS: [RegExp, string][] = [
  [/Android/, 'Android'],
  [/iPhone|iPad|iOS/, 'iOS'],
  [/Mac OS X|Macintosh/, 'macOS'],
  [/Windows/, 'Windows'],
  [/CrOS/, 'ChromeOS'],
  [/Linux/, 'Linux'],
];

/** A short device description from a User-Agent ("Chrome · macOS"), or null when unknown. */
export function device(userAgent: string | null): string | null {
  if (!userAgent) return null;
  const browser = BROWSERS.find(([re]) => re.test(userAgent))?.[1];
  const system = SYSTEMS.find(([re]) => re.test(userAgent))?.[1];
  if (!browser && !system) return userAgent.length > 60 ? `${userAgent.slice(0, 60)}…` : userAgent;
  return [browser, system].filter(Boolean).join(' · ');
}
