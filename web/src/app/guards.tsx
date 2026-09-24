import { useEffect, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Navigate, Outlet, useLocation, useParams } from 'react-router';
import { LoadingState, ErrorState } from '@/components/common/States';
import { useListOrganizations } from '@/lib/api/generated/organizations/organizations';
import type { Role } from '@/lib/api/generated/model';
import { NotFoundPage } from '@/pages/NotFoundPage';
import { useSession } from '@/auth/session';
import { safeNext } from './navigation';
import { hasRole, lastOrg, OrgContext, rememberOrg, useCurrentOrg } from './org';

function FullPageLoading() {
  return (
    <div className="mx-auto max-w-md p-8">
      <LoadingState rows={2} />
    </div>
  );
}

/** Routes for signed-in users; others go to /login?next=<current path>. */
export function RequireAuth() {
  const { status, endedBy } = useSession();
  const location = useLocation();
  if (status === 'loading') return <FullPageLoading />;
  if (status === 'anonymous') {
    if (endedBy === 'signout') return <Navigate to="/login" replace />;
    const next = location.pathname + location.search;
    return <Navigate to={`/login?next=${encodeURIComponent(next)}`} replace />;
  }
  return <Outlet />;
}

/** Sign-in pages: signed-in users are sent to their destination instead. */
export function RedirectIfAuthenticated() {
  const { status } = useSession();
  const location = useLocation();
  if (status === 'loading') return <FullPageLoading />;
  if (status === 'authenticated') return <Navigate to={safeNext(new URLSearchParams(location.search).get('next'))} replace />;
  return <Outlet />;
}

/** "/" → the last used organization, the first one, or onboarding. */
export function RootRedirect() {
  const orgs = useListOrganizations({ limit: 200 });
  if (orgs.isPending) return <FullPageLoading />;
  if (orgs.isError) {
    return (
      <div className="mx-auto max-w-md p-8">
        <ErrorState error={orgs.error} onRetry={() => void orgs.refetch()} />
      </div>
    );
  }
  const items = orgs.data.items;
  if (items.length === 0) return <Navigate to="/onboarding" replace />;
  const remembered = lastOrg();
  const target = items.find((o) => o.slug === remembered) ?? items[0];
  return <Navigate to={`/o/${target?.slug ?? ''}`} replace />;
}

/** Resolves :orgSlug among the user's organizations; unknown slugs are a 404 (no tenant leak). */
export function OrgRoute() {
  const { orgSlug } = useParams();
  const orgs = useListOrganizations({ limit: 200 });
  const org = orgs.data?.items.find((o) => o.slug === orgSlug);
  useEffect(() => {
    if (org) rememberOrg(org.slug);
  }, [org]);
  if (orgs.isPending) return <LoadingState />;
  if (orgs.isError) return <ErrorState error={orgs.error} onRetry={() => void orgs.refetch()} />;
  if (!org) return <NotFoundPage />;
  return (
    <OrgContext.Provider value={org}>
      <Outlet />
    </OrgContext.Provider>
  );
}

/** Renders children only for members with at least the given role (UI convenience; the API enforces it). */
export function RequireRole({ min, children }: { min: Role; children: ReactNode }) {
  const org = useCurrentOrg();
  const { t } = useTranslation();
  if (!hasRole(org.role, min)) {
    return (
      <div className="space-y-2 py-16 text-center">
        <h1 className="text-2xl font-bold">{t('forbidden.title')}</h1>
        <p className="text-muted-foreground">{t('forbidden.body')}</p>
      </div>
    );
  }
  return <>{children}</>;
}
