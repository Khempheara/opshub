import { useMatch } from 'react-router';
import { useListOrganizations } from '@/lib/api/generated/organizations/organizations';
import type { Organization, Role } from '@/lib/api/generated/model';
import { lastOrg } from './org';

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

/**
 * The organization the sidebar shows links for: the one in the URL, or on account-level pages
 * (/settings/…) the last one visited. Its role comes from the same cached organization list,
 * so role-dependent links (Runners, Audit log) don't disappear on account pages. The role is
 * undefined until the list has loaded, or when the remembered organization is gone.
 */
export function useNavOrg(): { slug: string; role?: Role } | null {
  const match = useMatch('/o/:orgSlug/*');
  const slug = match?.params.orgSlug ?? lastOrg();
  const orgs = useListOrganizations({ limit: 200 }, { query: { enabled: slug !== null } });
  if (!slug) return null;
  return { slug, role: orgs.data?.items.find((o) => o.slug === slug)?.role };
}
