import type { ProjectDora, ProjectPipelines, RunStatus } from '@/lib/api/generated/model';

export const PERIODS = ['7d', '30d', '90d', '365d'] as const;
export type Period = (typeof PERIODS)[number];
const PERIOD_DAYS: Record<Period, number> = { '7d': 7, '30d': 30, '90d': 90, '365d': 365 };

/** The query range for a period ending at `now` (ms). */
export function periodRange(period: Period, now: number): { from: string; to: string } {
  return { from: new Date(now - PERIOD_DAYS[period] * 86_400_000).toISOString(), to: new Date(now).toISOString() };
}

/** DORA performance levels (State of DevOps benchmarks). */
export type Level = 'elite' | 'high' | 'medium' | 'low';

const DAY = 86_400;

/** Elite: daily or more; high: weekly; medium: monthly; low: less. Null without deployments. */
export function frequencyLevel(perDay: number): Level | null {
  if (perDay <= 0) return null;
  if (perDay >= 1) return 'elite';
  if (perDay >= 1 / 7) return 'high';
  if (perDay >= 1 / 30) return 'medium';
  return 'low';
}

/** Elite: under a day; high: under a week; medium: under a month; low: longer. */
export function leadTimeLevel(seconds: number | null): Level | null {
  if (seconds === null) return null;
  if (seconds < DAY) return 'elite';
  if (seconds < 7 * DAY) return 'high';
  if (seconds < 30 * DAY) return 'medium';
  return 'low';
}

/** Elite: up to 5 %; high: 10 %; medium: 15 %; low: more. */
export function failureRateLevel(rate: number | null): Level | null {
  if (rate === null) return null;
  if (rate <= 0.05) return 'elite';
  if (rate <= 0.1) return 'high';
  if (rate <= 0.15) return 'medium';
  return 'low';
}

/** Elite: under an hour; high: under a day; medium: under a week; low: longer. */
export function restoreLevel(seconds: number | null): Level | null {
  if (seconds === null) return null;
  if (seconds < 3600) return 'elite';
  if (seconds < DAY) return 'high';
  if (seconds < 7 * DAY) return 'medium';
  return 'low';
}

/** Deployment frequency in the most readable unit: per day, per week or per month. */
export function frequency(perDay: number): { unit: 'perDay' | 'perWeek' | 'perMonth'; value: number } {
  if (perDay >= 1) return { unit: 'perDay', value: perDay };
  if (perDay * 7 >= 1) return { unit: 'perWeek', value: perDay * 7 };
  return { unit: 'perMonth', value: perDay * 30 };
}

/** One project's row: its pipeline and deployment figures side by side. */
export interface ProjectRow {
  id: string;
  name: string;
  slug: string;
  runs: number;
  successRate: number | null;
  durationP50: number | null;
  lastStatus: RunStatus | null;
  changes: number;
  changeFailureRate: number | null;
  leadTime: number | null;
  lastActivity: string | null;
}

/** Joins the per-project lists of both endpoints, busiest first (runs + changes). */
export function projectRows(pipelines: readonly ProjectPipelines[], dora: readonly ProjectDora[]): ProjectRow[] {
  const rows = new Map<string, ProjectRow>();
  const row = (id: string, name: string, slug: string) => {
    let r = rows.get(id);
    if (!r) {
      r = { id, name, slug, runs: 0, successRate: null, durationP50: null, lastStatus: null, changes: 0, changeFailureRate: null, leadTime: null, lastActivity: null };
      rows.set(id, r);
    }
    return r;
  };
  const latest = (a: string | null, b: string) => (a === null || b > a ? b : a);
  for (const p of pipelines) {
    const r = row(p.id, p.name, p.slug);
    Object.assign(r, { runs: p.runs, successRate: p.success_rate, durationP50: p.duration_p50_s, lastStatus: p.last_status });
    r.lastActivity = latest(r.lastActivity, p.last_run_at);
  }
  for (const d of dora) {
    const r = row(d.id, d.name, d.slug);
    Object.assign(r, { changes: d.changes, changeFailureRate: d.change_failure_rate, leadTime: d.lead_time_median_s });
    r.lastActivity = latest(r.lastActivity, d.last_deployed_at);
  }
  return [...rows.values()].sort((a, b) => b.runs + b.changes - (a.runs + a.changes) || a.name.localeCompare(b.name));
}

/** The top of a bar chart's scale: 1, 2 or 5 × 10ⁿ at or above max. */
export function niceMax(max: number): number {
  if (!Number.isFinite(max) || max <= 0) return 1;
  const p = 10 ** Math.floor(Math.log10(max));
  for (const m of [1, 2, 5, 10]) if (m * p >= max) return m * p;
  return 10 * p;
}
