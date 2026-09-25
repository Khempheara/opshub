import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router';
import { useCurrentOrg } from '@/app/org';
import { PageHeader } from '@/components/common/PageHeader';
import { EmptyState, ErrorState, LoadingState } from '@/components/common/States';
import { Card } from '@/components/ui/card';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table';
import { selectClass } from '@/features/org/constants';
import { useFormat } from '@/i18n/useFormat';
import { useListCertificates } from '@/lib/api/generated/infrastructure/infrastructure';
import { InfraTabs } from './InfraTabs';
import { AssetStatusLabel } from './status';

const WINDOWS = ['', '7d', '14d', '30d', '90d'] as const;

/** /o/:orgSlug/infrastructure/certificates — every domain's certificate, soonest expiry first. */
export function CertificatesPage() {
  const { t } = useTranslation('infra');
  const org = useCurrentOrg();
  const fmt = useFormat();
  const [within, setWithin] = useState<(typeof WINDOWS)[number]>('');
  const certs = useListCertificates(org.id, { expiring_within: within || undefined });

  return (
    <div className="space-y-6">
      <PageHeader title={t('title')} description={t('certificates.description')} />
      <InfraTabs />
      <div className="flex flex-wrap items-center gap-2">
        <label className="text-sm" htmlFor="cert-within">
          {t('certificates.filter')}
        </label>
        <select
          id="cert-within"
          className={`${selectClass} w-auto`}
          value={within}
          onChange={(e) => {
            setWithin(e.target.value as (typeof WINDOWS)[number]);
          }}
        >
          {WINDOWS.map((w) => (
            <option key={w} value={w}>
              {w ? t(`certificates.within.${w}`) : t('certificates.within.all')}
            </option>
          ))}
        </select>
      </div>
      {certs.isPending ? (
        <LoadingState />
      ) : certs.isError ? (
        <ErrorState error={certs.error} onRetry={() => void certs.refetch()} />
      ) : certs.data.items.length === 0 ? (
        <EmptyState title={within ? t('certificates.emptyFiltered') : t('certificates.empty')} description={within ? undefined : t('certificates.emptyHint')} />
      ) : (
        <Card className="gap-0 overflow-x-auto p-0">
          <Table data-testid="certificates-table">
            <TableHeader>
              <TableRow>
                <TableHead>{t('certificates.columns.domain')}</TableHead>
                <TableHead>{t('certificates.columns.status')}</TableHead>
                <TableHead>{t('certificates.columns.expires')}</TableHead>
                <TableHead>{t('certificates.columns.issuer')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {certs.data.items.map((c) => (
                <TableRow key={c.asset_id} data-testid="certificate-row">
                  <TableCell>
                    <Link to={`/o/${org.slug}/infrastructure/${c.asset_id}`} className="font-medium break-all hover:underline">
                      {c.asset_name}
                    </Link>
                    <span className="text-muted-foreground block font-mono text-xs" dir="ltr">
                      {c.host}
                      {c.port !== 443 ? `:${String(c.port)}` : ''}
                    </span>
                  </TableCell>
                  <TableCell>
                    <AssetStatusLabel status={c.status} />
                    {c.error && <p className="text-destructive mt-1 max-w-64 text-xs">{c.error}</p>}
                  </TableCell>
                  <TableCell className="whitespace-nowrap">
                    {c.not_after ? (
                      <>
                        {fmt.dateTime(c.not_after, { dateStyle: 'medium' })}
                        {c.days_left !== null && <span className="text-muted-foreground block text-xs">{t('certificates.daysLeft', { count: c.days_left })}</span>}
                      </>
                    ) : (
                      '—'
                    )}
                  </TableCell>
                  <TableCell className="text-muted-foreground max-w-64 text-xs break-words" dir="ltr">
                    {c.issuer || '—'}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Card>
      )}
    </div>
  );
}
