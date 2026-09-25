import { describe, expect, it } from 'vitest';
import type { Asset } from '@/lib/api/generated/model';
import { emptyAssetForm, fromAsset, parseMetadata, parseTags, toRequest, type AssetForm } from './assetForm';

const form = (over: Partial<AssetForm>): AssetForm => ({ ...emptyAssetForm, ...over });

describe('parseTags', () => {
  it('splits on commas and whitespace and drops empties', () => {
    expect(parseTags(' prod, web\n region:sg ,,  ')).toEqual(['prod', 'web', 'region:sg']);
    expect(parseTags('')).toEqual([]);
  });
});

describe('parseMetadata', () => {
  it('reads KEY=value lines and keeps "=" inside values', () => {
    expect(parseMetadata('provider = hetzner\n\nnote=a=b\n')).toEqual({ provider: 'hetzner', note: 'a=b' });
  });
  it('keeps a line without "=" as a key so the server can report it', () => {
    expect(parseMetadata('oops')).toEqual({ oops: '' });
  });
});

describe('toRequest', () => {
  it('sends kind only on create and trims fields', () => {
    const f = form({ kind: 'domain', name: ' shop ', address: ' shop.example.com ', tags: 'prod', tlsPort: '8443' });
    expect(toRequest(f, true)).toEqual({
      kind: 'domain',
      name: 'shop',
      address: 'shop.example.com',
      description: '',
      tags: ['prod'],
      metadata: {},
      tls_port: 8443,
    });
    expect(toRequest(f, false)).not.toHaveProperty('kind');
  });
  it('defaults an empty port to 443 and marks a non-number as invalid', () => {
    expect(toRequest(form({ tlsPort: '' }), true).tls_port).toBe(443);
    expect(toRequest(form({ tlsPort: 'abc' }), true).tls_port).toBe(-1);
  });
});

describe('fromAsset', () => {
  it('round-trips through the form', () => {
    const a = {
      id: 'a1',
      kind: 'server',
      name: 'web-1',
      address: '10.0.0.12',
      description: 'Frontend',
      tags: ['prod', 'web'],
      metadata: { provider: 'hetzner', rack: 'b2' },
      tls_port: 443,
      status: 'no_agent',
      agent: null,
      metrics: null,
      certificate: null,
      version: 3,
      created_at: '2026-09-01T00:00:00Z',
      updated_at: '2026-09-01T00:00:00Z',
    } satisfies Asset;
    const f = fromAsset(a);
    expect(f).toEqual({ kind: 'server', name: 'web-1', address: '10.0.0.12', description: 'Frontend', tags: 'prod, web', metadata: 'provider=hetzner\nrack=b2', tlsPort: '443' });
    const r = toRequest(f, false);
    expect(r.tags).toEqual(a.tags);
    expect(r.metadata).toEqual(a.metadata);
  });
});
