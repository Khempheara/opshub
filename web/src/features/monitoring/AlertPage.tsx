import { useQueryClient } from '@tanstack/react-query';
import { ArrowLeft, BellOff, CheckCheck } from 'lucide-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useParams } from 'react-router';
import { toast } from 'sonner';
import { useCurrentOrg } from '@/app/org';
import { OrgAction, usePermissions } from '@/app/permissions';
import { ErrorState, LoadingState } from '@/components/common/States';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { useFormat } from '@/i18n/useFormat';
import { errorMessage, hasCode } from '@/lib/api/errors';
import type { AlertEvent } from '@/lib/api/generated/model';
import { acknowledgeAlert, getGetAlertQueryKey, getListAlertsQueryKey, useGetAlert, useListAlertRules } from '@/lib/api/generated/monitoring/monitoring';
import { NotFoundPage } from '@/pages/NotFoundPage';
import { AlertSummary } from './AlertSummary';
import { SilenceDialog } from './SilenceDialog';
import { AlertStateLabel, SeverityBadge } from './status';

function EventText({ e }: { e: AlertEvent }) {
  const { t } = useTranslation('monitoring');
  const values = { channel: e.channel_name, user: e.user_name ?? '—', detail: e.detail };
  switch (e.kind) {
    case 'notified':
      return <>{t('events.notified', values)}</>;
    case 'notify_failed':
      return <>{t('events.notify_failed', values)}</>;
    case 'escalated':
      return <>{t('events.escalated', values)}</>;
    case 'acknowledged':
      return <>{t('events.acknowledged', values)}</>;
    case 'silenced':
      return <>{t('events.silenced')}</>;
    case 'resolved':
      return <>{e.detail ? t('events.resolvedWhy', values) : t('events.resolved')}</>;
    default:
      return <>{t('events.fired')}</>;
  }
}

/** /o/:orgSlug/monitoring/alerts/:alertId */
export function AlertPage() {
  const { t } = useTranslation(['monitoring', 'common']);
  const { alertId = '' } = useParams();
  const org = useCurrentOrg();
  const perms = usePermissions(org.id);
  const fmt = useFormat();
  const queryClient = useQueryClient();
  const [silencing, setSilencing] = useState(false);
  const alert = useGetAlert(alertId, { query: { refetchInterval: 15_000 } });
  const rules = useListAlertRules(org.id, { query: { enabled: silencing } });

  if (alert.isPending) return <LoadingState />;
  if (alert.isError) {
    if (hasCode(alert.error, 'ALERT_NOT_FOUND')) return <NotFoundPage />;
    return <ErrorState error={alert.error} onRetry={() => void alert.refetch()} />;
  }
  const a = alert.data;
  const firing = a.status === 'firing';
  const subjectLink =
    a.subject_type === 'monitor' ? `/o/${org.slug}/monitoring/monitors/${a.subject_id}` : `/o/${org.slug}/infrastructure/${a.subject_id}`;

  const ack = async () => {
    try {
      await acknowledgeAlert(a.id);
      await queryClient.invalidateQueries({ queryKey: getGetAlertQueryKey(a.id) });
      await queryClient.invalidateQueries({ queryKey: getListAlertsQueryKey(org.id) });
      toast.success(t('alerts.ackDone'));
    } catch (err) {
      toast.error(errorMessage(err));
    }
  };

  return (
    <div className="space-y-6">
      <Link to={`/o/${org.slug}/monitoring/alerts`} className="text-muted-foreground hover:text-foreground inline-flex items-center gap-1 text-sm">
        <ArrowLeft aria-hidden className="size-4 rtl:rotate-180" />
        {t('alerts.back')}
      </Link>
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div className="min-w-0 space-y-2">
          <h1 className="text-2xl font-semibold tracking-tight break-words">
            {a.rule_name}
            {a.rule_id === null && <span className="text-muted-foreground ms-2 text-sm font-normal">{t('alerts.ruleDeleted')}</span>}
          </h1>
          <div className="flex flex-wrap items-center gap-3">
            <SeverityBadge severity={a.severity} />
            <AlertStateLabel alert={a} />
          </div>
          <AlertSummary alert={a} className="block break-words" />
        </div>
        {firing && (
          <div className="flex flex-wrap gap-2">
            {perms.can(OrgAction.alertAck) && !a.acknowledged_at && (
              <Button onClick={() => void ack()} title={t('alerts.ackHint')}>
                <CheckCheck aria-hidden />
                {t('alerts.acknowledge')}
              </Button>
            )}
            {perms.can(OrgAction.monitorManage) && (
              <Button
                variant="outline"
                onClick={() => {
                  setSilencing(true);
                }}
              >
                <BellOff aria-hidden />
                {t('alerts.silence')}
              </Button>
            )}
          </div>
        )}
      </div>

      <Card>
        <CardContent className="grid gap-3 text-sm sm:grid-cols-2">
          <div>
            <p className="text-muted-foreground">{t('alerts.startedAt')}</p>
            <p>{a.started_at ? fmt.dateTime(a.started_at, { dateStyle: 'medium', timeStyle: 'short' }) : '—'}</p>
          </div>
          <div>
            <p className="text-muted-foreground">{t('alerts.resolvedAt')}</p>
            <p>{a.resolved_at ? fmt.dateTime(a.resolved_at, { dateStyle: 'medium', timeStyle: 'short' }) : t('alerts.ongoing')}</p>
          </div>
          {a.acknowledged_at && (
            <p className="sm:col-span-2">
              {t('alerts.acknowledged', { name: a.acknowledged_by_name ?? '—', time: fmt.relative(a.acknowledged_at) })}
            </p>
          )}
          <p className="sm:col-span-2">
            <Link to={subjectLink} className="text-primary hover:underline">
              {t('alerts.viewSubject', { name: a.subject_name })}
            </Link>
          </p>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>{t('alerts.timeline')}</CardTitle>
        </CardHeader>
        <CardContent>
          <ol className="space-y-3 border-s ps-4" data-testid="alert-timeline">
            {a.events.map((e, i) => (
              <li key={`${e.at}-${String(i)}`} className="text-sm" data-kind={e.kind}>
                <span className="text-muted-foreground block text-xs">{fmt.dateTime(e.at, { dateStyle: 'short', timeStyle: 'medium' })}</span>
                <span className={e.kind === 'notify_failed' ? 'text-destructive' : undefined}>
                  <EventText e={e} />
                </span>
              </li>
            ))}
          </ol>
        </CardContent>
      </Card>

      {silencing && (
        <SilenceDialog
          orgId={org.id}
          rules={rules.data?.items ?? []}
          subject={{ ruleId: a.rule_id, id: a.subject_id, name: a.subject_name }}
          onClose={() => {
            setSilencing(false);
          }}
        />
      )}
    </div>
  );
}
