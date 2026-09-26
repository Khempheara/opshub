import { describe, expect, it } from 'vitest';
import { auditParams, categoryOf, changes, device, emptyAuditFilters, exportFileName, metadataEntries } from './auditView';

describe('auditParams', () => {
  const anchor = Date.UTC(2026, 8, 26, 12, 0, 0);
  it('counts the period back from the anchor', () => {
    expect(auditParams(emptyAuditFilters, anchor, 'en')).toEqual({ locale: 'en', from: '2026-08-27T12:00:00.000Z' });
  });
  it('maps a category to its areas and passes the other filters', () => {
    expect(auditParams({ category: 'monitoring', actor: 'u1', project: 'p1', period: 'all' }, anchor, 'km')).toEqual({
      locale: 'km', area: ['monitor', 'alert_rule', 'alert', 'silence', 'channel'], actor: 'u1', project: 'p1',
    });
    expect(auditParams(emptyAuditFilters, anchor, 'fr').locale).toBe('en');
  });
});

describe('categoryOf', () => {
  it('finds the group of an area', () => {
    expect(categoryOf('alert_rule')).toBe('monitoring');
    expect(categoryOf('member')).toBe('organization');
    expect(categoryOf('auth')).toBeUndefined();
  });
});

describe('changes', () => {
  it('lists changed keys only', () => {
    expect(changes({ name: 'QA', slug: 'qa', tags: ['a'] }, { name: 'ក្រុម QA', slug: 'qa', tags: ['a', 'b'] })).toEqual([
      { key: 'name', before: 'QA', after: 'ក្រុម QA' },
      { key: 'tags', before: '["a"]', after: '["a","b"]' },
    ]);
  });
  it('shows created and deleted values', () => {
    expect(changes(null, { role: 'developer' })).toEqual([{ key: 'role', after: 'developer' }]);
    expect(changes({ name: 'web' }, null)).toEqual([{ key: 'name', before: 'web' }]);
    expect(changes({ enabled: true }, {})).toEqual([{ key: 'enabled', before: 'true' }]);
    expect(changes(null, null)).toEqual([]);
  });
});

describe('metadataEntries', () => {
  it('sorts and hides the project id', () => {
    expect(metadataEntries({ z: 1, project_id: 'p', a: { b: 2 } })).toEqual([
      ['a', '{"b":2}'],
      ['z', '1'],
    ]);
  });
});

describe('device', () => {
  it('names common browsers and systems', () => {
    expect(device('Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0 Safari/537.36')).toBe('Chrome · macOS');
    expect(device('Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/140.0 Safari/537.36 Edg/140.0')).toBe('Edge · Windows');
    expect(device('Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 Version/18.0 Mobile Safari/604.1')).toBe('Safari · iOS');
    expect(device('Mozilla/5.0 (X11; Linux x86_64; rv:130.0) Gecko/20100101 Firefox/130.0')).toBe('Firefox · Linux');
    expect(device('curl/8.7.1')).toBe('curl');
  });
  it('falls back to the raw text', () => {
    expect(device(null)).toBeNull();
    expect(device('custom-agent')).toBe('custom-agent');
    expect(device('x'.repeat(80))).toBe(`${'x'.repeat(60)}…`);
  });
});

describe('exportFileName', () => {
  it('matches the server', () => {
    expect(exportFileName('angkor-tech', new Date(Date.UTC(2026, 8, 6, 23, 0)))).toBe('audit-log-angkor-tech-20260906.csv');
  });
});
