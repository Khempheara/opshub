import { useTranslation } from 'react-i18next';
import { NavLink } from 'react-router';
import { useCurrentOrg } from '@/app/org';
import { OrgAction, usePermissions } from '@/app/permissions';
import { cn } from '@/lib/utils';

/** Search / Ingest tokens (Admins). */
export function LogsTabs() {
  const { t } = useTranslation('logs');
  const org = useCurrentOrg();
  const perms = usePermissions(org.id);
  if (!perms.can(OrgAction.logsManage)) return null;
  const base = `/o/${org.slug}/logs`;
  const tabs = [
    { to: base, label: t('tabs.search'), end: true },
    { to: `${base}/tokens`, label: t('tabs.tokens'), end: false },
  ];
  return (
    <nav aria-label={t('tabs.label')} className="-mx-1 overflow-x-auto overflow-y-hidden border-b">
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
  );
}
