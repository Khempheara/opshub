import { useTranslation } from 'react-i18next';
import { NavLink } from 'react-router';
import { useCurrentOrg } from '@/app/org';
import { cn } from '@/lib/utils';

/** Assets / Certificates tabs of the Infrastructure section. */
export function InfraTabs() {
  const { t } = useTranslation('infra');
  const org = useCurrentOrg();
  const base = `/o/${org.slug}/infrastructure`;
  const tabs = [
    { to: base, label: t('tabs.assets'), end: true },
    { to: `${base}/certificates`, label: t('tabs.certificates'), end: false },
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
