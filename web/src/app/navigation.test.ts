import { describe, expect, it } from 'vitest';
import { slugify } from '@/features/org/slug';
import { safeNext } from './navigation';
import { hasRole } from './org';

describe('safeNext', () => {
  it('allows same-site paths only', () => {
    expect(safeNext('/settings/profile?tab=1')).toBe('/settings/profile?tab=1');
    for (const bad of [null, '', 'https://evil.example', '//evil.example', '/\\evil.example', 'javascript:alert(1)']) {
      expect(safeNext(bad)).toBe('/');
    }
  });
});

describe('slugify', () => {
  it('matches the server rules', () => {
    expect(slugify('Angkor Tech')).toBe('angkor-tech');
    expect(slugify('  Mekong -- Labs!! ')).toBe('mekong-labs');
    expect(slugify('អង្គរ')).toBe('');
    expect(slugify('Angkor Tech · អង្គរ')).toBe('angkor-tech');
  });
});

describe('hasRole', () => {
  it('orders roles owner > admin > developer > viewer', () => {
    expect(hasRole('owner', 'admin')).toBe(true);
    expect(hasRole('developer', 'developer')).toBe(true);
    expect(hasRole('viewer', 'developer')).toBe(false);
    expect(hasRole('admin', 'owner')).toBe(false);
  });
});
