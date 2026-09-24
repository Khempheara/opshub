import { Building2, Check, ChevronsUpDown, Plus } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router';
import { lastOrg } from '@/app/org';
import { useRouteOrg } from '@/app/useRouteOrg';
import { Button } from '@/components/ui/button';
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu';
import { useListOrganizations } from '@/lib/api/generated/organizations/organizations';

export function OrgSwitcher() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const orgs = useListOrganizations({ limit: 200 });
  // On account pages (settings) show the last organization used.
  const routeOrg = useRouteOrg();
  const current = routeOrg ?? orgs.data?.items.find((o) => o.slug === lastOrg()) ?? null;

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="outline" className="min-w-0 max-w-[14rem] shrink justify-between gap-2" aria-label={t('org.switcher')}>
          <Building2 aria-hidden />
          <span className="truncate-khmer min-w-0 flex-1 text-start">{current?.name ?? t('org.none')}</span>
          <ChevronsUpDown aria-hidden className="opacity-60" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="w-64">
        <DropdownMenuLabel>{t('org.switcher')}</DropdownMenuLabel>
        {orgs.data?.items.map((o) => (
          <DropdownMenuItem
            key={o.id}
            onSelect={() => {
              void navigate(`/o/${o.slug}`);
            }}
          >
            <span className="min-w-0 flex-1 truncate-khmer">{o.name}</span>
            <span className="text-muted-foreground text-xs">{t(`roles.${o.role}`)}</span>
            {o.id === current?.id && <Check aria-label={t('org.current')} className="size-4" />}
          </DropdownMenuItem>
        ))}
        <DropdownMenuSeparator />
        <DropdownMenuItem
          onSelect={() => {
            void navigate('/onboarding');
          }}
        >
          <Plus aria-hidden className="size-4" />
          {t('org.create')}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
