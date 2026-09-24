import { createContext, useContext } from 'react';
import type { Organization, Role } from '@/lib/api/generated/model';

export const ROLE_RANK: Record<Role, number> = { viewer: 1, developer: 2, admin: 3, owner: 4 };

/** True when role is at least min (owner > admin > developer > viewer). */
export function hasRole(role: Role, min: Role): boolean {
  return ROLE_RANK[role] >= ROLE_RANK[min];
}

/** Provided by the /o/:orgSlug route (see OrgRoute). */
export const OrgContext = createContext<Organization | null>(null);

/** The organization of the current /o/:orgSlug route. */
export function useCurrentOrg(): Organization {
  const org = useContext(OrgContext);
  if (!org) throw new Error('useCurrentOrg must be used inside an organization route');
  return org;
}

const LAST_ORG_KEY = 'opshub.lastOrg';

export function rememberOrg(slug: string): void {
  try {
    localStorage.setItem(LAST_ORG_KEY, slug);
  } catch {
    // not critical
  }
}

export function lastOrg(): string | null {
  try {
    return localStorage.getItem(LAST_ORG_KEY);
  } catch {
    return null;
  }
}
