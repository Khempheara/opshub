import { Plus } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router';
import { PageHeader } from '@/components/common/PageHeader';
import { EmptyState, ErrorState, LoadingState } from '@/components/common/States';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card } from '@/components/ui/card';
import { useListOrganizations } from '@/lib/api/generated/organizations/organizations';

export function OrganizationsPage() {
  const { t } = useTranslation(['settings', 'common']);
  const orgs = useListOrganizations({ limit: 200 });
  const create = (
    <Button asChild>
      <Link to="/onboarding">
        <Plus aria-hidden />
        {t('common:org.create')}
      </Link>
    </Button>
  );

  return (
    <div className="space-y-6">
      <PageHeader title={t('organizations.title')} description={t('organizations.description')} actions={create} />
      {orgs.isPending ? (
        <LoadingState />
      ) : orgs.isError ? (
        <ErrorState error={orgs.error} onRetry={() => void orgs.refetch()} />
      ) : orgs.data.items.length === 0 ? (
        <EmptyState title={t('organizations.empty')} action={create} />
      ) : (
        <Card className="gap-0 p-0">
          <ul className="divide-y">
            {orgs.data.items.map((o) => (
              <li key={o.id} className="flex flex-wrap items-center gap-3 px-4 py-3">
                <div className="min-w-0 flex-1">
                  <p className="font-medium">{o.name}</p>
                  <p className="text-muted-foreground font-mono text-xs">{o.slug}</p>
                </div>
                <Badge variant="secondary">{t(`common:roles.${o.role}`)}</Badge>
                <Button asChild variant="outline" size="sm">
                  <Link to={`/o/${o.slug}`}>{t('organizations.open')}</Link>
                </Button>
              </li>
            ))}
          </ul>
        </Card>
      )}
    </div>
  );
}
