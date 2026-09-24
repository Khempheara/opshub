import { describe, expect, it } from 'vitest';
import type { Environment } from '@/lib/api/generated/model';
import { emptyEnvironmentForm, parseBranches, toFormValues, toRequest } from './envForm';

const env: Environment = {
  id: 'e1',
  project_id: 'p1',
  name: 'production',
  kind: 'production',
  variables: { REGION: 'ap-southeast-1', LOG_LEVEL: 'warn' },
  protection: { required_approvals: 2, allowed_branches: ['main', 'release/*'], allowed_roles: ['admin'] },
  version: 3,
  created_at: '2026-09-25T00:00:00Z',
  updated_at: '2026-09-25T00:00:00Z',
};

describe('environment form', () => {
  it('round-trips an environment', () => {
    const v = toFormValues(env);
    expect(v.variables).toEqual([
      { key: 'LOG_LEVEL', value: 'warn' },
      { key: 'REGION', value: 'ap-southeast-1' },
    ]);
    expect(v.branches).toBe('main\nrelease/*');
    expect(toRequest(v)).toEqual({
      name: 'production',
      kind: 'production',
      variables: { LOG_LEVEL: 'warn', REGION: 'ap-southeast-1' },
      protection: { required_approvals: 2, allowed_branches: ['main', 'release/*'], allowed_roles: ['admin'] },
    });
  });

  it('sends null protection when unprotected and drops blank variable rows', () => {
    const v = { ...emptyEnvironmentForm(), name: ' qa ', kind: 'staging' as const, variables: [{ key: ' ', value: 'x' }, { key: 'A', value: '1' }, { key: 'A', value: '2' }] };
    expect(toRequest(v)).toEqual({ name: 'qa', kind: 'staging', variables: { A: '2' }, protection: null });
  });

  it('parses branch patterns from lines or commas', () => {
    expect(parseBranches(' main \n\nrelease/*, main,hotfix/* ')).toEqual(['main', 'release/*', 'hotfix/*']);
    expect(parseBranches('')).toEqual([]);
  });

  it('defaults new environments to Owner/Admin approval roles', () => {
    expect(toFormValues({ ...env, protection: null }).roles).toEqual(['owner', 'admin']);
  });
});
