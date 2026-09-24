import { describe, expect, it } from 'vitest';
import { assignableRoles, canManageMember } from './permissions';

// These mirror authz.MaxAssignableRole and authz.CanManageMember (internal/authz); the server
// enforces the same rules, the UI only uses them to hide controls.
describe('assignableRoles', () => {
  it('lets owners grant any role', () => {
    expect(assignableRoles('owner')).toEqual(['owner', 'admin', 'developer', 'viewer']);
  });
  it.each(['admin', 'developer', 'viewer'] as const)('limits %s to developer and viewer', (role) => {
    expect(assignableRoles(role)).toEqual(['developer', 'viewer']);
  });
});

describe('canManageMember', () => {
  const me = 'me';
  it('lets owners manage everyone', () => {
    for (const role of ['owner', 'admin', 'developer', 'viewer'] as const) {
      expect(canManageMember('owner', me, { role, user_id: 'x' })).toBe(true);
    }
  });
  it('lets admins manage developers and viewers only', () => {
    expect(canManageMember('admin', me, { role: 'developer', user_id: 'x' })).toBe(true);
    expect(canManageMember('admin', me, { role: 'viewer', user_id: 'x' })).toBe(true);
    expect(canManageMember('admin', me, { role: 'admin', user_id: 'x' })).toBe(false);
    expect(canManageMember('admin', me, { role: 'owner', user_id: 'x' })).toBe(false);
  });
  it('lets anyone manage themselves (leave, step down)', () => {
    expect(canManageMember('admin', me, { role: 'admin', user_id: me })).toBe(true);
    expect(canManageMember('viewer', me, { role: 'viewer', user_id: me })).toBe(true);
  });
  it('gives developers and viewers no control over others', () => {
    expect(canManageMember('developer', me, { role: 'viewer', user_id: 'x' })).toBe(false);
    expect(canManageMember('viewer', me, { role: 'viewer', user_id: 'x' })).toBe(false);
  });
});
