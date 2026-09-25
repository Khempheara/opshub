import { describe, expect, it } from 'vitest';
import type { Environment } from '@/lib/api/generated/model';
import { creatableScopes, nameProblem, normalizeName, usageSnippet, valueTooLarge, MAX_VALUE_BYTES } from './secretForm';

const env = (name: string, isProtected: boolean): Environment => ({
  id: name,
  project_id: 'p',
  name,
  kind: 'staging',
  variables: {},
  protection: isProtected ? { required_approvals: 0, allowed_branches: [], allowed_roles: ['owner', 'admin'] } : null,
  version: 1,
  created_at: '',
  updated_at: '',
});

describe('names', () => {
  it('normalizes typing into an environment variable name', () => {
    expect(normalizeName('stripe api-key')).toBe('STRIPE_API_KEY');
    expect(normalizeName('db--url')).toBe('DB_URL');
  });
  it('reports invalid and reserved names', () => {
    expect(nameProblem('DB_URL')).toBeNull();
    expect(nameProblem('_X')).toBeNull();
    expect(nameProblem('9LIVES')).toBe('secret_name');
    expect(nameProblem('')).toBe('secret_name');
    expect(nameProblem('db_url')).toBe('secret_name');
    expect(nameProblem('OPSHUB_TOKEN')).toBe('reserved');
  });
});

describe('valueTooLarge', () => {
  it('counts bytes, not characters', () => {
    expect(valueTooLarge('x'.repeat(MAX_VALUE_BYTES))).toBe(false);
    expect(valueTooLarge('x'.repeat(MAX_VALUE_BYTES + 1))).toBe(true);
    expect(valueTooLarge('ក'.repeat(MAX_VALUE_BYTES / 3 + 1))).toBe(true);
  });
});

describe('creatableScopes', () => {
  const envs = [env('staging', false), env('production', true)];
  it('lets admins create anywhere', () => {
    expect(creatableScopes(envs, 'admin')).toEqual({ all: true, environments: envs });
    expect(creatableScopes(envs, 'owner').all).toBe(true);
  });
  it('limits developers to unprotected environments', () => {
    expect(creatableScopes(envs, 'developer')).toEqual({ all: false, environments: [envs[0]] });
  });
});

describe('usageSnippet', () => {
  it('shows how a job lists the secret', () => {
    expect(usageSnippet('DB_URL', null)).toBe('jobs:\n  deploy:\n    secrets: [DB_URL]');
    expect(usageSnippet('DB_URL', 'staging')).toBe('jobs:\n  deploy:\n    environment: staging\n    secrets: [DB_URL]');
  });
});
