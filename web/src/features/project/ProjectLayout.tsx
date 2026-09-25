import { ArrowLeft } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { Link, NavLink, Outlet, useLocation, useParams } from 'react-router';
import { useCurrentOrg } from '@/app/org';
import { ErrorState, LoadingState } from '@/components/common/States';
import { RoleBadge } from '@/features/org/roles';
import { hasCode } from '@/lib/api/errors';
import { useGetProject } from '@/lib/api/generated/projects/projects';
import { cn } from '@/lib/utils';
import { NotFoundPage } from '@/pages/NotFoundPage';
import { ProjectContext } from './context';

/** Loads the project for /o/:orgSlug/projects/:projectId/* and renders its header and tabs. */
export function ProjectLayout() {
  const { t } = useTranslation(['project', 'pipeline']);
  const { projectId = '' } = useParams();
  const { pathname } = useLocation();
  const org = useCurrentOrg();
  const project = useGetProject(projectId, { query: { retry: (n, err) => !hasCode(err, 'PROJECT_NOT_FOUND') && n < 2 } });

  if (project.isPending) return <LoadingState />;
  if (project.isError) {
    if (hasCode(project.error, 'PROJECT_NOT_FOUND')) return <NotFoundPage />;
    return <ErrorState error={project.error} onRetry={() => void project.refetch()} />;
  }
  const p = project.data;
  // A project of another organization the user belongs to: treat the URL as wrong.
  if (p.organization_id !== org.id) return <NotFoundPage />;

  const base = `/o/${org.slug}/projects/${p.id}`;
  const tabs = [
    // The Pipelines tab covers the run list (index) and run pages.
    { to: base, label: t('pipeline:tab'), end: !pathname.startsWith(`${base}/runs/`) },
    { to: `${base}/environments`, label: t('detail.tabs.environments') },
    { to: `${base}/repository`, label: t('detail.tabs.repository') },
    { to: `${base}/access`, label: t('detail.tabs.access') },
    { to: `${base}/settings`, label: t('detail.tabs.settings') },
  ];

  return (
    <ProjectContext.Provider value={p}>
      <div className="space-y-6">
        <div className="space-y-3">
          <Link to={`/o/${org.slug}/projects`} className="text-muted-foreground hover:text-foreground inline-flex items-center gap-1 text-sm">
            <ArrowLeft aria-hidden className="size-4 rtl:rotate-180" />
            {t('detail.back')}
          </Link>
          <div className="flex flex-wrap items-center gap-3">
            <h1 className="min-w-0 text-2xl font-bold break-words">{p.name}</h1>
            <span className="text-muted-foreground inline-flex items-center gap-1.5 text-sm">
              <span className="sr-only">{t('detail.yourRole', { role: p.role })}</span>
              <RoleBadge role={p.role} />
            </span>
          </div>
          {p.description && <p className="text-muted-foreground break-words">{p.description}</p>}
        </div>
        <nav aria-label={t('detail.tabs.label')} className="-mx-1 overflow-x-auto border-b">
          <ul className="flex min-w-max gap-1 px-1">
            {tabs.map((tab) => (
              <li key={tab.to}>
                <NavLink
                  to={tab.to}
                  end={tab.end}
                  className={({ isActive }) =>
                    cn(
                      'focus-visible:ring-ring/50 -mb-px inline-block rounded-t-md border-b-2 px-3 py-2 text-sm outline-none focus-visible:ring-[3px]',
                      isActive ? 'border-primary text-foreground font-medium' : 'text-muted-foreground hover:text-foreground border-transparent',
                    )
                  }
                >
                  {tab.label}
                </NavLink>
              </li>
            ))}
          </ul>
        </nav>
        <Outlet />
      </div>
    </ProjectContext.Provider>
  );
}
