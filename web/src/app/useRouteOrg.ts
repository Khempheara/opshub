import { useMatch } from 'react-router';
import { useListOrganizations } from '@/lib/api/generated/organizations/organizations';
import type { Organization } from '@/lib/api/generated/model';

/**
 * The organization named by the current /o/:orgSlug URL, for layout components (top bar,
 * sidebar) that render outside the org route's context. Null on account-level pages.
 */
export function useRouteOrg(): Organization | null {
  const match = useMatch('/o/:orgSlug/*');
  const orgs = useListOrganizations({ limit: 200 }, { query: { enabled: match !== null } });
  if (!match) return null;
  return orgs.data?.items.find((o) => o.slug === match.params.orgSlug) ?? null;
}
