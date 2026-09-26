import { z } from 'zod';
import type { LogEntry, LogLevel, LogSource, SearchLogsParams } from '@/lib/api/generated/model';

export const RANGES = ['15m', '1h', '6h', '24h', '7d'] as const;
export type Range = (typeof RANGES)[number];

const RANGE_MS: Record<Range, number> = {
  '15m': 15 * 60_000,
  '1h': 60 * 60_000,
  '6h': 6 * 60 * 60_000,
  '24h': 24 * 60 * 60_000,
  '7d': 7 * 24 * 60 * 60_000,
};

export const LEVELS: readonly LogLevel[] = ['debug', 'info', 'warn', 'error'];
export const SOURCES: readonly LogSource[] = ['service', 'job', 'deployment'];

/** Applied search filters; empty strings are unset. */
export interface Filters {
  q: string;
  level: LogLevel | '';
  source: LogSource | '';
  service: string;
  range: Range;
}

export const emptyFilters: Filters = { q: '', level: '', source: '', service: '', range: '1h' };

/** Query parameters for a search run at `anchor` (ms). The end is left open (the server's now). */
export function searchParams(f: Filters, anchor: number, limit = 200): SearchLogsParams {
  const p: SearchLogsParams = { from: new Date(anchor - RANGE_MS[f.range]).toISOString(), limit };
  const q = f.q.trim();
  if (q) p.q = q;
  if (f.level) p.level = f.level;
  if (f.source) p.source = f.source;
  if (f.service) p.service = f.service;
  return p;
}

/** At most this many lines are kept while following; older ones are dropped. */
export const FOLLOW_CAP = 2000;

/**
 * Newest-first lines: `newer` goes on top of `current`, without duplicates, capped. `trimmed`
 * tells the caller that older lines were dropped (paging further back would leave a gap).
 */
export function mergeNewer(current: readonly LogEntry[], newer: readonly LogEntry[], cap = FOLLOW_CAP): { items: LogEntry[]; trimmed: boolean } {
  const seen = new Set<string>();
  const items: LogEntry[] = [];
  for (const e of [...newer, ...current]) {
    if (seen.has(e.id)) continue;
    seen.add(e.id);
    items.push(e);
  }
  return items.length > cap ? { items: items.slice(0, cap), trimmed: true } : { items, trimmed: false };
}

/** Older lines appended below, without duplicates. */
export function appendOlder(current: readonly LogEntry[], older: readonly LogEntry[]): LogEntry[] {
  const seen = new Set(current.map((e) => e.id));
  return [...current, ...older.filter((e) => !seen.has(e.id))];
}

export const levelClass: Record<LogLevel, string> = {
  debug: 'text-muted-foreground border-muted-foreground/30',
  info: 'text-sky-700 dark:text-sky-300 border-sky-500/40',
  warn: 'text-amber-700 dark:text-amber-300 border-amber-500/50',
  error: 'text-destructive border-destructive/50',
};

/** Attributes as sorted key/value text: strings as they are, everything else as JSON. */
export function attributeEntries(attrs: Record<string, unknown>): [string, string][] {
  return Object.keys(attrs)
    .sort()
    .map((k) => {
      const v = attrs[k];
      return [k, typeof v === 'string' ? v : JSON.stringify(v)];
    });
}

/** A copy-paste example that sends one line with a new ingest token. */
export function curlExample(origin: string, token: string, service: string): string {
  const line = JSON.stringify({ level: 'info', message: `Hello from ${service}` });
  return [
    `curl -X POST ${origin}/api/v1/ingest/logs \\`,
    `  -H "Authorization: Bearer ${token}" \\`,
    `  -H "Content-Type: application/x-ndjson" \\`,
    `  --data-binary '${line}'`,
  ].join('\n');
}

/** Mirrors the API's service-name rule. */
export const SERVICE_PATTERN = /^[a-z0-9][a-z0-9._/-]{0,99}$/;

export const tokenSchema = z.object({
  name: z.string().trim().min(1, 'errors:rules.required').max(100, 'errors:rules.max'),
  service: z.string().trim().regex(SERVICE_PATTERN, 'logs:tokens.serviceInvalid'),
});
export type TokenValues = z.infer<typeof tokenSchema>;
