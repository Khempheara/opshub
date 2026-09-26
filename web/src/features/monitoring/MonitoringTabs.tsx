import { useTranslation } from 'react-i18next';
import { NavLink } from 'react-router';
import { useCurrentOrg } from '@/app/org';
import { OrgAction, usePermissions } from '@/app/permissions';
import { cn } from '@/lib/utils';

/** Monitors / Alerts / Rules / Silences / Channels. */
export function MonitoringTabs() {
  const { t } = useTranslation('monitoring');
  const org = useCurrentOrg();
  const perms = usePermissions(org.id);
  const base = `/o/${org.slug}/monitoring`;
  const tabs = [
    { to: base, label: t('tabs.monitors'), end: true },
    { to: `${base}/alerts`, label: t('tabs.alerts'), end: false },
    { to: `${base}/rules`, label: t('tabs.rules'), end: false },
    { to: `${base}/silences`, label: t('tabs.silences'), end: false },
    // Channels are for Developers and up (channel.view).
    ...(perms.can(OrgAction.channelView) ? [{ to: `${base}/channels`, label: t('tabs.channels'), end: false }] : []),
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
