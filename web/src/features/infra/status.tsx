import { useTranslation } from 'react-i18next';
import type { AssetStatus } from '@/lib/api/generated/model';
import { cn } from '@/lib/utils';

const tones: Record<AssetStatus, string> = {
  online: 'bg-success',
  valid: 'bg-success',
  offline: 'bg-destructive',
  expired: 'bg-destructive',
  error: 'bg-destructive',
  expiring: 'bg-amber-500',
  pending: 'bg-sky-500',
  no_agent: 'bg-muted-foreground/50',
  unchecked: 'bg-muted-foreground/50',
  inventory: 'bg-muted-foreground/30',
};

/** A dot and the translated status. */
export function AssetStatusLabel({ status }: { status: AssetStatus }) {
  const { t } = useTranslation('infra');
  return (
    <span className="inline-flex items-center gap-2 text-sm whitespace-nowrap" data-status={status}>
      <span aria-hidden className={cn('size-2 shrink-0 rounded-full', tones[status])} />
      {t(`status.${status}`)}
    </span>
  );
}

/** A percentage bar with its value (— when unknown). */
export function UsageBar({ label, value, formatted }: { label: string; value: number | null; formatted: string }) {
  const tone = value === null ? '' : value >= 90 ? 'bg-destructive' : value >= 75 ? 'bg-amber-500' : 'bg-success';
  return (
    <div className="min-w-16" title={`${label} ${formatted}`}>
      <div className="text-muted-foreground flex justify-between gap-1 text-[11px]">
        <span className="truncate">{label}</span>
        <span className="shrink-0 tabular-nums">{formatted}</span>
      </div>
      <div className="bg-muted h-1.5 overflow-hidden rounded-full" aria-hidden>
        {value !== null && <div className={cn('h-full rounded-full', tone)} style={{ width: `${String(Math.min(100, Math.max(0, value)))}%` }} />}
      </div>
    </div>
  );
}
