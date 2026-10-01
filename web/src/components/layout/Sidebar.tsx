import { Activity, Boxes, Building2, FolderGit2, History, KeyRound, LayoutDashboard, Rocket, ScrollText, Server, Settings, ShieldCheck, UserRound, Users, UsersRound, type LucideIcon } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { NavLink } from 'react-router';
import { hasRole } from '@/app/org';
import { useNavOrg } from '@/app/useRouteOrg';
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip';
import { cn } from '@/lib/utils';

interface Item {
  to: string;
  label: string;
  icon: LucideIcon;
  end?: boolean;
}

/** Navigation groups; feature modules add their entries here as they ship. */
function useNavGroups(): { label?: string; items: Item[] }[] {
  const { t } = useTranslation(['common', 'org', 'project']);
  const org = useNavOrg();
  const groups: { label?: string; items: Item[] }[] = [];
  if (org) {
    const base = `/o/${org.slug}`;
    groups.push({
      items: [
        { to: base, label: t('nav.overview'), icon: LayoutDashboard, end: true },
        { to: `${base}/projects`, label: t('project:nav.projects'), icon: FolderGit2 },
      ],
    });
    groups.push({
      label: t('org:nav.group'),
      items: [
        { to: `${base}/members`, label: t('org:nav.members'), icon: Users },
        { to: `${base}/teams`, label: t('org:nav.teams'), icon: UsersRound },
        // runner.view is Developer and up (the page itself also guards).
        ...(org.role && hasRole(org.role, 'developer') ? [{ to: `${base}/runners`, label: t('org:nav.runners'), icon: Server }] : []),
        { to: `${base}/deploy-targets`, label: t('org:nav.deployTargets'), icon: Rocket },
        { to: `${base}/infrastructure`, label: t('org:nav.infrastructure'), icon: Boxes },
        { to: `${base}/monitoring`, label: t('org:nav.monitoring'), icon: Activity },
        { to: `${base}/logs`, label: t('org:nav.logs'), icon: ScrollText },
        // audit.view is Owner/Admin (the page itself also guards).
        ...(org.role && hasRole(org.role, 'admin') ? [{ to: `${base}/audit-log`, label: t('org:nav.audit'), icon: History }] : []),
        { to: `${base}/settings`, label: t('org:nav.settings'), icon: Settings },
      ],
    });
  }
  groups.push({
    label: t('nav.settings'),
    items: [
      { to: '/settings/profile', label: t('nav.profile'), icon: UserRound },
      { to: '/settings/security', label: t('nav.security'), icon: ShieldCheck },
      { to: '/settings/tokens', label: t('nav.tokens'), icon: KeyRound },
      { to: '/settings/organizations', label: t('nav.organizations'), icon: Building2 },
    ],
  });
  return groups;
}

/** Sidebar navigation. `collapsed` shows icons only (labels become tooltips). */
export function SidebarNav({ collapsed = false, onNavigate }: { collapsed?: boolean; onNavigate?: () => void }) {
  const { t } = useTranslation();
  const groups = useNavGroups();
  return (
    <nav aria-label={t('nav.main')} className="flex flex-col gap-4 p-2">
      {groups.map((g, gi) => (
        <div key={g.label ?? gi} className="flex flex-col gap-1">
          {g.label && !collapsed && <p className="text-muted-foreground px-3 pt-2 text-xs font-medium">{g.label}</p>}
          {g.items.map((item) => {
            const link = (
              <NavLink
                key={item.to}
                to={item.to}
                end={item.end}
                onClick={onNavigate}
                aria-label={collapsed ? item.label : undefined}
                className={({ isActive }) =>
                  cn(
                    'flex min-h-9 items-center gap-3 rounded-md px-3 py-1.5 text-sm transition-colors',
                    isActive ? 'bg-accent text-accent-foreground font-medium' : 'text-muted-foreground hover:bg-accent/60 hover:text-foreground',
                    collapsed && 'justify-center px-0',
                  )
                }
              >
                <item.icon aria-hidden className="size-4 shrink-0" />
                {!collapsed && <span className="min-w-0">{item.label}</span>}
              </NavLink>
            );
            return collapsed ? (
              <Tooltip key={item.to}>
                <TooltipTrigger asChild>{link}</TooltipTrigger>
                <TooltipContent side="right">{item.label}</TooltipContent>
              </Tooltip>
            ) : (
              link
            );
          })}
        </div>
      ))}
    </nav>
  );
}
