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
import { AlertSeverity, ListAlertsStatus, type Alert } from '@/lib/api/generated/model';
import { useListAlerts } from '@/lib/api/generated/monitoring/monitoring';
import { AlertSummary } from './AlertSummary';
import { MonitoringTabs } from './MonitoringTabs';
import { AlertStateLabel, SeverityBadge } from './status';

function Duration({ alert }: { alert: Alert }) {
  const { t } = useTranslation('monitoring');
  const fmt = useFormat();
  if (!alert.started_at) return null;
  if (!alert.resolved_at) return <>{t('alerts.ongoing')}</>;
  return <>{fmt.duration(Date.parse(alert.resolved_at) - Date.parse(alert.started_at))}</>;
}

/** Organization → Monitoring → Alerts. */
export function AlertsPage() {
  const { t } = useTranslation(['monitoring', 'common']);
  const org = useCurrentOrg();
  const fmt = useFormat();
  const [status, setStatus] = useState('');
  const [severity, setSeverity] = useState('');
  const alerts = useListAlerts(
    org.id,
    { status: (status || undefined) as ListAlertsStatus | undefined, severity: (severity || undefined) as AlertSeverity | undefined },
    { query: { refetchInterval: 15_000 } },
  );
  const filtered = Boolean(status || severity);

  return (
    <div className="space-y-6">
      <PageHeader title={t('title')} description={t('description')} />
      <MonitoringTabs />
      <div className="flex flex-wrap gap-2">
        <label className="sr-only" htmlFor="alert-status">
          {t('alerts.filterStatus')}
        </label>
        <select
          id="alert-status"
          className={`${selectClass} w-auto`}
          value={status}
          onChange={(e) => {
            setStatus(e.target.value);
          }}
        >
          <option value="">{t('alerts.all')}</option>
          {Object.values(ListAlertsStatus).map((s) => (
            <option key={s} value={s}>
              {t(`alertStatus.${s}`)}
            </option>
          ))}
        </select>
        <label className="sr-only" htmlFor="alert-severity">
          {t('alerts.filterSeverity')}
        </label>
        <select
          id="alert-severity"
          className={`${selectClass} w-auto`}
          value={severity}
          onChange={(e) => {
            setSeverity(e.target.value);
          }}
        >
          <option value="">{t('alerts.allSeverities')}</option>
          {Object.values(AlertSeverity).map((s) => (
            <option key={s} value={s}>
              {t(`severity.${s}`)}
            </option>
          ))}
        </select>
      </div>
      {alerts.isPending ? (
        <LoadingState />
      ) : alerts.isError ? (
        <ErrorState error={alerts.error} onRetry={() => void alerts.refetch()} />
      ) : alerts.data.items.length === 0 ? (
        <EmptyState title={filtered ? t('alerts.emptyFiltered') : t('alerts.empty')} description={filtered ? undefined : t('alerts.emptyHint')} />
      ) : (
        <Card className="gap-0 overflow-x-auto p-0">
          <Table data-testid="alerts-table">
            <TableHeader>
              <TableRow>
                <TableHead>{t('alerts.columns.alert')}</TableHead>
                <TableHead>{t('alerts.columns.severity')}</TableHead>
                <TableHead>{t('alerts.columns.started')}</TableHead>
                <TableHead>{t('alerts.columns.duration')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {alerts.data.items.map((a) => (
                <TableRow key={a.id} data-testid="alert-row">
                  <TableCell className="max-w-96 whitespace-normal">
                    <Link to={`/o/${org.slug}/monitoring/alerts/${a.id}`} className="font-medium break-words hover:underline">
                      {a.rule_name}
                    </Link>
                    <AlertSummary alert={a} className="text-muted-foreground block text-xs break-words" />
                    <span className="mt-1 block">
                      <AlertStateLabel alert={a} />
                    </span>
                  </TableCell>
                  <TableCell>
                    <SeverityBadge severity={a.severity} />
                  </TableCell>
                  <TableCell className="text-xs whitespace-nowrap">{a.started_at ? fmt.relative(a.started_at) : '—'}</TableCell>
                  <TableCell className="text-xs whitespace-nowrap">
                    <Duration alert={a} />
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
