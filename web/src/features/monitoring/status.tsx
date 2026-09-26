import { useTranslation } from 'react-i18next';
import { useFormat } from '@/i18n/useFormat';
import type { Alert, AlertSeverity, MonitorPoint, MonitorStatus as Status } from '@/lib/api/generated/model';
import { cn } from '@/lib/utils';

const statusTone: Record<Status, string> = {
  up: 'bg-success',
  down: 'bg-destructive',
  pending: 'bg-sky-500',
  paused: 'bg-muted-foreground/50',
};

/** A dot and the translated monitor status. */
export function MonitorStatusLabel({ status }: { status: Status }) {
  const { t } = useTranslation('monitoring');
  return (
    <span className="inline-flex items-center gap-2 text-sm whitespace-nowrap" data-status={status}>
      <span aria-hidden className={cn('size-2 shrink-0 rounded-full', statusTone[status])} />
      {t(`status.${status}`)}
    </span>
  );
}

const severityTone: Record<AlertSeverity, string> = {
  info: 'bg-sky-500/10 text-sky-700 dark:text-sky-300',
  warning: 'bg-amber-500/10 text-amber-700 dark:text-amber-300',
  critical: 'bg-rose-500/10 text-rose-700 dark:text-rose-300',
};

export function SeverityBadge({ severity }: { severity: AlertSeverity }) {
  const { t } = useTranslation('monitoring');
  return <span className={cn('rounded-md px-2 py-0.5 text-xs font-medium whitespace-nowrap', severityTone[severity])}>{t(`severity.${severity}`)}</span>;
}

/** Firing / resolved, with acknowledged and silenced marks. */
export function AlertStateLabel({ alert }: { alert: Alert }) {
  const { t } = useTranslation('monitoring');
  const firing = alert.status === 'firing';
  return (
    <span className="inline-flex flex-wrap items-center gap-x-2 gap-y-1 text-sm" data-status={alert.status}>
      <span className="inline-flex items-center gap-2 whitespace-nowrap">
        <span aria-hidden className={cn('size-2 shrink-0 rounded-full', firing ? 'bg-destructive' : 'bg-success')} />
        {t(`alertStatus.${alert.status}`)}
      </span>
      {firing && alert.acknowledged_at && <span className="text-muted-foreground text-xs">{t('alertStatus.acknowledged')}</span>}
      {firing && alert.silenced && <span className="text-muted-foreground text-xs">{t('alertStatus.silenced')}</span>}
    </span>
  );
}

function stripTone(ratio: number | null): string {
  if (ratio === null) return 'bg-muted';
  if (ratio === 1) return 'bg-success';
  return ratio >= 0.9 ? 'bg-amber-500' : 'bg-destructive';
}

/**
 * One bar per step of the range, coloured by the share of successful checks (a status-page
 * strip). Steps without checks stay grey, so a new monitor doesn't look like a full day up.
 */
export function UptimeStrip({ points, from, to, stepSeconds }: { points: MonitorPoint[]; from: number; to: number; stepSeconds: number }) {
  const { t } = useTranslation('monitoring');
  const fmt = useFormat();
  const step = stepSeconds * 1000;
  const slots = Math.min(Math.max(Math.ceil((to - from) / step), 1), 300);
  const byIndex = new Map<number, MonitorPoint>();
  for (const p of points) byIndex.set(Math.floor((Date.parse(p.t) - from) / step), p);
  // Columns shrink to fit any width; gaps only while each slot is wide enough to show them.
  return (
    <div
      className={cn('grid h-8 items-stretch', slots <= 100 && 'gap-px')}
      style={{ gridTemplateColumns: `repeat(${String(slots)}, minmax(0, 1fr))` }}
      aria-hidden
    >
      {Array.from({ length: slots }, (_, i) => {
        const p = byIndex.get(i);
        const ratio = p && p.checks > 0 ? p.up_checks / p.checks : null;
        return (
          <span
            key={i}
            className={cn('min-w-0', slots <= 100 && 'rounded-[1px]', stripTone(ratio))}
            title={
              p
                ? t('monitors.charts.bucket', { time: fmt.dateTime(p.t, { dateStyle: 'short', timeStyle: 'short' }), up: p.up_checks, checks: p.checks })
                : undefined
            }
          />
        );
      })}
    </div>
  );
}
