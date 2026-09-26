import { describe, expect, it } from 'vitest';
import type { ProjectDora, ProjectPipelines } from '@/lib/api/generated/model';
import { failureRateLevel, frequency, frequencyLevel, leadTimeLevel, niceMax, periodRange, projectRows, restoreLevel } from './dashboardView';

describe('periodRange', () => {
  it('counts days back from now', () => {
    const now = Date.UTC(2026, 8, 26, 12);
    expect(periodRange('7d', now)).toEqual({ from: '2026-09-19T12:00:00.000Z', to: '2026-09-26T12:00:00.000Z' });
    expect(periodRange('365d', now).from).toBe('2025-09-26T12:00:00.000Z');
  });
});

describe('DORA levels', () => {
  it('rates deployment frequency', () => {
    expect(frequencyLevel(0)).toBeNull();
    expect(frequencyLevel(3)).toBe('elite');
    expect(frequencyLevel(1 / 3)).toBe('high');
    expect(frequencyLevel(1 / 20)).toBe('medium');
    expect(frequencyLevel(1 / 60)).toBe('low');
  });
  it('rates lead time', () => {
    expect(leadTimeLevel(null)).toBeNull();
    expect(leadTimeLevel(3600)).toBe('elite');
    expect(leadTimeLevel(2 * 86400)).toBe('high');
    expect(leadTimeLevel(10 * 86400)).toBe('medium');
    expect(leadTimeLevel(45 * 86400)).toBe('low');
  });
  it('rates change failure rate', () => {
    expect(failureRateLevel(null)).toBeNull();
    expect(failureRateLevel(0)).toBe('elite');
    expect(failureRateLevel(0.05)).toBe('elite');
    expect(failureRateLevel(0.08)).toBe('high');
    expect(failureRateLevel(0.15)).toBe('medium');
    expect(failureRateLevel(0.4)).toBe('low');
  });
  it('rates time to restore', () => {
    expect(restoreLevel(null)).toBeNull();
    expect(restoreLevel(600)).toBe('elite');
    expect(restoreLevel(5 * 3600)).toBe('high');
    expect(restoreLevel(3 * 86400)).toBe('medium');
    expect(restoreLevel(8 * 86400)).toBe('low');
  });
});

describe('frequency', () => {
  it('picks a readable unit', () => {
    expect(frequency(2.5)).toEqual({ unit: 'perDay', value: 2.5 });
    expect(frequency(0.5)).toEqual({ unit: 'perWeek', value: 3.5 });
    expect(frequency(0.05).unit).toBe('perMonth');
    expect(frequency(0.05).value).toBeCloseTo(1.5);
  });
});

describe('projectRows', () => {
  it('joins both lists, busiest first', () => {
    const pipelines = [
      { id: 'a', name: 'Alpha', slug: 'alpha', runs: 4, success_rate: 0.5, duration_p50_s: 60, last_status: 'failed', last_run_at: '2026-09-20T00:00:00Z' },
      { id: 'b', name: 'Beta', slug: 'beta', runs: 1, success_rate: 1, duration_p50_s: 30, last_status: 'succeeded', last_run_at: '2026-09-25T00:00:00Z' },
    ] as ProjectPipelines[];
    const dora = [
      { id: 'a', name: 'Alpha', slug: 'alpha', changes: 3, change_failure_rate: 0.33, lead_time_median_s: 7200, last_deployed_at: '2026-09-22T00:00:00Z' },
      { id: 'c', name: 'Gamma', slug: 'gamma', changes: 1, change_failure_rate: 0, lead_time_median_s: null, last_deployed_at: '2026-09-01T00:00:00Z' },
    ] as ProjectDora[];
    const rows = projectRows(pipelines, dora);
    expect(rows.map((r) => r.id)).toEqual(['a', 'b', 'c']);
    expect(rows[0]).toMatchObject({ runs: 4, changes: 3, leadTime: 7200, lastActivity: '2026-09-22T00:00:00Z', lastStatus: 'failed' });
    expect(rows[2]).toMatchObject({ runs: 0, successRate: null, changes: 1, lastStatus: null });
  });
});

describe('niceMax', () => {
  it('rounds up', () => {
    expect(niceMax(0)).toBe(1);
    expect(niceMax(7)).toBe(10);
    expect(niceMax(12)).toBe(20);
    expect(niceMax(300)).toBe(500);
  });
});
