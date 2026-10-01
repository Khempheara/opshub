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
import { ListMonitorsStatus } from '@/lib/api/generated/model';
import { useListMonitors } from '@/lib/api/generated/monitoring/monitoring';
import { MonitorDialog } from './MonitorDialog';
import { MonitoringTabs } from './MonitoringTabs';
import { MonitorStatusLabel } from './status';

/** Organization → Monitoring: uptime monitors. */
export function MonitorsPage() {
  const { t } = useTranslation(['monitoring', 'common']);
  const org = useCurrentOrg();
  const fmt = useFormat();
  const perms = usePermissions(org.id);
  const canManage = perms.can(OrgAction.monitorManage);
  const [search, setSearch] = useState('');
  const [status, setStatus] = useState('');
  const [adding, setAdding] = useState(false);
  const monitors = useListMonitors(
    org.id,
    { q: search.trim() || undefined, status: (status || undefined) as ListMonitorsStatus | undefined },
    { query: { refetchInterval: 15_000 } },
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
              {t('monitors.add')}
            </Button>
          ) : undefined
        }
      />
      <MonitoringTabs />
      <div className="flex flex-wrap gap-2">
        <label className="sr-only" htmlFor="monitor-search">
          {t('monitors.search')}
        </label>
        <Input
          id="monitor-search"
          className="max-w-xs"
          placeholder={t('monitors.search')}
          value={search}
          onChange={(e) => {
            setSearch(e.target.value);
          }}
        />
        <label className="sr-only" htmlFor="monitor-status">
          {t('monitors.filterStatus')}
        </label>
        <select
          id="monitor-status"
          className={`${selectClass} w-auto`}
          value={status}
          onChange={(e) => {
            setStatus(e.target.value);
          }}
        >
          <option value="">{t('monitors.allStatuses')}</option>
          {Object.values(ListMonitorsStatus).map((s) => (
            <option key={s} value={s}>
              {t(`status.${s}`)}
            </option>
          ))}
        </select>
      </div>
      {monitors.isPending ? (
        <LoadingState />
      ) : monitors.isError ? (
        <ErrorState error={monitors.error} onRetry={() => void monitors.refetch()} />
      ) : monitors.data.items.length === 0 ? (
        <EmptyState title={t('monitors.empty')} description={canManage ? t('monitors.emptyHint') : undefined} />
      ) : (
        <Card className="gap-0 overflow-x-auto p-0">
          <Table data-testid="monitors-table">
            <TableHeader>
              <TableRow>
                <TableHead>{t('monitors.columns.name')}</TableHead>
                <TableHead>{t('monitors.columns.status')}</TableHead>
                <TableHead>{t('monitors.columns.uptime')}</TableHead>
                <TableHead>{t('monitors.columns.latency')}</TableHead>
                <TableHead>{t('monitors.columns.checked')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {monitors.data.items.map((m) => (
                <TableRow key={m.id} data-testid="monitor-row">
                  <TableCell className="max-w-80 min-w-48 whitespace-normal">
                    <Link to={`/o/${org.slug}/monitoring/monitors/${m.id}`} className="font-medium break-words hover:underline">
                      {m.name}
                    </Link>
                    <span className="text-muted-foreground block truncate font-mono text-xs" dir="ltr" title={m.target}>
                      {t(`kinds.${m.kind}`)} · {m.target}
                    </span>
                    {m.labels.length > 0 && (
                      <span className="mt-1 flex flex-wrap gap-1">
                        {m.labels.map((l) => (
                          <Badge key={l} variant="secondary" className="font-mono">
                            {l}
                          </Badge>
                        ))}
                      </span>
                    )}
                  </TableCell>
                  <TableCell className="min-w-28 whitespace-normal">
                    <MonitorStatusLabel status={m.status} />
                    {m.status === 'down' && m.last_error && <p className="text-destructive mt-1 max-w-56 text-xs break-words">{m.last_error}</p>}
                  </TableCell>
                  <TableCell className="tabular-nums">{m.uptime_24h === null ? '—' : `${fmt.number(m.uptime_24h, { maximumFractionDigits: 2 })}%`}</TableCell>
                  <TableCell className="tabular-nums">
                    {m.last_latency_ms === null ? '—' : t('monitors.ms', { value: fmt.number(m.last_latency_ms) })}
                  </TableCell>
                  <TableCell className="text-muted-foreground text-xs whitespace-nowrap">
                    {m.last_checked_at ? fmt.relative(m.last_checked_at) : t('monitors.never')}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Card>
      )}
      {adding && (
        <MonitorDialog
          orgId={org.id}
          orgSlug={org.slug}
          onClose={() => {
            setAdding(false);
          }}
        />
      )}
    </div>
  );
}
