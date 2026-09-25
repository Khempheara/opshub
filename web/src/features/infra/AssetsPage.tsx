import { Plus } from 'lucide-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router';
import { useCurrentOrg } from '@/app/org';
import { OrgAction, usePermissions } from '@/app/permissions';
import { PageHeader } from '@/components/common/PageHeader';
import { EmptyState, ErrorState, LoadingState } from '@/components/common/States';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card } from '@/components/ui/card';
import { Input } from '@/components/ui/input';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table';
import { selectClass } from '@/features/org/constants';
import { useFormat } from '@/i18n/useFormat';
import { AssetKind, AssetStatus, type Asset } from '@/lib/api/generated/model';
import { useListAssets } from '@/lib/api/generated/infrastructure/infrastructure';
import { AssetDialog } from './AssetDialog';
import { InfraTabs } from './InfraTabs';
import { kindIcon } from './kinds';
import { AssetStatusLabel, UsageBar } from './status';

function Health({ a }: { a: Asset }) {
  const { t } = useTranslation('infra');
  const fmt = useFormat();
  const pct = (v: number | null | undefined) => (v === null || v === undefined ? '—' : `${fmt.number(v, { maximumFractionDigits: 0 })}%`);
  if (a.kind === 'server' && a.metrics && a.status === 'online') {
    return (
      <div className="grid w-64 grid-cols-3 gap-3">
        <UsageBar label={t('metrics.cpu')} value={a.metrics.cpu_pct} formatted={pct(a.metrics.cpu_pct)} />
        <UsageBar label={t('metrics.mem')} value={a.metrics.mem_pct} formatted={pct(a.metrics.mem_pct)} />
        <UsageBar label={t('metrics.disk')} value={a.metrics.disk_pct} formatted={pct(a.metrics.disk_pct)} />
      </div>
    );
  }
  if (a.kind === 'server' && a.agent?.last_heartbeat_at) {
    return <span className="text-muted-foreground text-xs">{t('assets.lastSeen', { time: fmt.relative(a.agent.last_heartbeat_at) })}</span>;
  }
  if (a.kind === 'domain' && a.certificate?.not_after) {
    return <span className="text-muted-foreground text-xs">{t('certificates.expires', { date: fmt.dateTime(a.certificate.not_after, { dateStyle: 'medium' }) })}</span>;
  }
  return null;
}

/** Organization → Infrastructure: the asset inventory. */
export function AssetsPage() {
  const { t } = useTranslation(['infra', 'common']);
  const org = useCurrentOrg();
  const perms = usePermissions(org.id);
  const canManage = perms.can(OrgAction.infraManage);
  const [kind, setKind] = useState('');
  const [status, setStatus] = useState('');
  const [search, setSearch] = useState('');
  const [adding, setAdding] = useState(false);
  const assets = useListAssets(
    org.id,
    { kind: (kind || undefined) as AssetKind | undefined, status: (status || undefined) as AssetStatus | undefined, q: search.trim() || undefined },
    { query: { refetchInterval: 30_000 } },
  );

  return (
    <div className="space-y-6">
      <PageHeader
        title={t('title')}
        description={t('description')}
        actions={
          canManage ? (
            <Button
              onClick={() => {
                setAdding(true);
              }}
            >
              <Plus aria-hidden />
              {t('assets.add')}
            </Button>
          ) : undefined
        }
      />
      <InfraTabs />
      <div className="flex flex-wrap gap-2">
        <label className="sr-only" htmlFor="asset-search">
          {t('assets.search')}
        </label>
        <Input
          id="asset-search"
          className="max-w-xs"
          placeholder={t('assets.search')}
          value={search}
          onChange={(e) => {
            setSearch(e.target.value);
          }}
        />
        <label className="sr-only" htmlFor="asset-kind">
          {t('assets.kind')}
        </label>
        <select
          id="asset-kind"
          className={`${selectClass} w-auto`}
          value={kind}
          onChange={(e) => {
            setKind(e.target.value);
          }}
        >
          <option value="">{t('assets.allKinds')}</option>
          {Object.values(AssetKind).map((k) => (
            <option key={k} value={k}>
              {t(`kinds.${k}`)}
            </option>
          ))}
        </select>
        <label className="sr-only" htmlFor="asset-status">
          {t('assets.statusFilter')}
        </label>
        <select
          id="asset-status"
          className={`${selectClass} w-auto`}
          value={status}
          onChange={(e) => {
            setStatus(e.target.value);
          }}
        >
          <option value="">{t('assets.allStatuses')}</option>
          {Object.values(AssetStatus).map((s) => (
            <option key={s} value={s}>
              {t(`status.${s}`)}
            </option>
          ))}
        </select>
      </div>
      {assets.isPending ? (
        <LoadingState />
      ) : assets.isError ? (
        <ErrorState error={assets.error} onRetry={() => void assets.refetch()} />
      ) : assets.data.items.length === 0 ? (
        <EmptyState title={t('assets.empty')} description={canManage ? t('assets.emptyManage') : undefined} />
      ) : (
        <Card className="gap-0 overflow-x-auto p-0">
          <Table data-testid="assets-table">
            <TableHeader>
              <TableRow>
                <TableHead>{t('assets.columns.name')}</TableHead>
                <TableHead>{t('assets.columns.address')}</TableHead>
                <TableHead>{t('assets.columns.tags')}</TableHead>
                <TableHead>{t('assets.columns.status')}</TableHead>
                <TableHead>{t('assets.columns.health')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {assets.data.items.map((a) => {
                const Icon = kindIcon[a.kind];
                return (
                  <TableRow key={a.id} data-testid="asset-row">
                    <TableCell>
                      <Link to={`/o/${org.slug}/infrastructure/${a.id}`} className="inline-flex items-center gap-2 font-medium hover:underline">
                        <Icon aria-hidden className="text-muted-foreground size-4 shrink-0" />
                        <span className="break-all">{a.name}</span>
                      </Link>
                      <span className="text-muted-foreground block ps-6 text-xs">{t(`kinds.${a.kind}`)}</span>
                    </TableCell>
                    <TableCell className="text-muted-foreground max-w-56 font-mono text-xs break-all" dir="ltr">
                      {a.address || '—'}
                    </TableCell>
                    <TableCell>
                      <div className="flex max-w-56 flex-wrap gap-1">
                        {a.tags.map((tag) => (
                          <Badge key={tag} variant="secondary" className="font-mono">
                            {tag}
                          </Badge>
                        ))}
                      </div>
                    </TableCell>
                    <TableCell>
                      <AssetStatusLabel status={a.status} />
                    </TableCell>
                    <TableCell>
                      <Health a={a} />
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        </Card>
      )}
      {adding && (
        <AssetDialog
          orgId={org.id}
          orgSlug={org.slug}
          open
          onOpenChange={(o) => {
            if (!o) setAdding(false);
          }}
        />
      )}
    </div>
  );
}
