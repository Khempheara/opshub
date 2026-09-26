import { describe, expect, it } from 'vitest';
import type { LogEntry } from '@/lib/api/generated/model';
import { appendOlder, attributeEntries, curlExample, emptyFilters, mergeNewer, searchParams, tokenSchema } from './logView';

const line = (id: string): LogEntry => ({
  id, ts: '2026-09-26T00:00:00Z', source: 'service', project_id: null, service: 'api', level: 'info', message: id, attributes: {},
});

describe('searchParams', () => {
  it('starts the range at the anchor and leaves the end open', () => {
    const anchor = Date.UTC(2026, 8, 26, 12, 0, 0);
    expect(searchParams(emptyFilters, anchor)).toEqual({ from: '2026-09-26T11:00:00.000Z', limit: 200 });
    expect(searchParams({ q: '  timeout ', level: 'warn', source: 'job', service: 'shop/build', range: '7d' }, anchor, 50)).toEqual({
      from: '2026-09-19T12:00:00.000Z', limit: 50, q: 'timeout', level: 'warn', source: 'job', service: 'shop/build',
    });
  });
});

describe('merging', () => {
  it('puts newer lines on top without duplicates', () => {
    expect(mergeNewer([line('b'), line('a')], [line('c'), line('b')]).items.map((e) => e.id)).toEqual(['c', 'b', 'a']);
  });
  it('caps and reports trimming', () => {
    const r = mergeNewer([line('b'), line('a')], [line('c')], 2);
    expect(r.items.map((e) => e.id)).toEqual(['c', 'b']);
    expect(r.trimmed).toBe(true);
    expect(mergeNewer([line('a')], [], 2).trimmed).toBe(false);
  });
  it('appends older lines below', () => {
    expect(appendOlder([line('c'), line('b')], [line('b'), line('a')]).map((e) => e.id)).toEqual(['c', 'b', 'a']);
  });
});

describe('attributeEntries', () => {
  it('sorts keys and stringifies values', () => {
    expect(attributeEntries({ z: 'text', a: 42, m: { deep: true }, n: null })).toEqual([
      ['a', '42'], ['m', '{"deep":true}'], ['n', 'null'], ['z', 'text'],
    ]);
  });
});

describe('curlExample', () => {
  it('sends one NDJSON line with the token', () => {
    const c = curlExample('https://ops.example.com', 'ohl_abc_secret', 'shop/api');
    expect(c).toContain('https://ops.example.com/api/v1/ingest/logs');
    expect(c).toContain('Bearer ohl_abc_secret');
    expect(c).toContain(`'{"level":"info","message":"Hello from shop/api"}'`);
  });
});

describe('tokenSchema', () => {
  it('mirrors the server rules', () => {
    expect(tokenSchema.safeParse({ name: 'API', service: 'shop/api-v2.worker_1' }).success).toBe(true);
    expect(tokenSchema.safeParse({ name: ' ', service: 'x' }).success).toBe(false);
    expect(tokenSchema.safeParse({ name: 'x', service: 'Shop API' }).success).toBe(false);
    expect(tokenSchema.safeParse({ name: 'x', service: '/leading' }).success).toBe(false);
  });
});
