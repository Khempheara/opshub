import { useGetOrganizationPermissions } from '@/lib/api/generated/organizations/organizations';
import type { Role } from '@/lib/api/generated/model';
import { hasRole } from './org';

/** Organization-scope actions (mirror internal/authz; the API's permissions endpoint lists them). */
export const OrgAction = {
  orgUpdate: 'org.update',
  orgDelete: 'org.delete',
  orgTransfer: 'org.transfer',
  memberInvite: 'member.invite',
  memberRemove: 'member.remove',
  memberUpdateRole: 'member.update_role',
  teamManage: 'team.manage',
  projectCreate: 'project.create',
  runnerView: 'runner.view',
  runnerManage: 'runner.manage',
} as const;

/**
 * The caller's permissions in an organization, from GET /orgs/{id}/permissions. The UI uses
 * them only to hide controls and guard routes; the API enforces every rule itself.
 */
export function usePermissions(orgId: string | undefined) {
  const q = useGetOrganizationPermissions(orgId ?? '', { query: { enabled: Boolean(orgId), staleTime: 60_000 } });
  const actions = new Set(q.data?.actions ?? []);
  return {
    isPending: q.isPending,
    role: q.data?.role,
    can: (action: string) => actions.has(action),
  };
}

/** Roles a member may grant (mirrors authz.MaxAssignableRole on the server). */
export function assignableRoles(myRole: Role): Role[] {
  return myRole === 'owner' ? ['owner', 'admin', 'developer', 'viewer'] : ['developer', 'viewer'];
}

/** Whether the caller may change or remove a member (mirrors authz.CanManageMember). */
export function canManageMember(myRole: Role, myUserId: string, target: { role: Role; user_id: string }): boolean {
  if (myRole === 'owner' || target.user_id === myUserId) return true;
  return myRole === 'admin' && !hasRole(target.role, 'admin');
}
