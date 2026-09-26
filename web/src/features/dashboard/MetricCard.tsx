import { useTranslation } from 'react-i18next';
import { Card } from '@/components/ui/card';
import { cn } from '@/lib/utils';
import type { Level } from './dashboardView';

const LEVEL_TONE: Record<Level, string> = {
  elite: 'bg-success/15 text-success border-success/40',
  high: 'bg-sky-500/10 text-sky-700 dark:text-sky-300 border-sky-500/40',
  medium: 'bg-amber-500/10 text-amber-700 dark:text-amber-300 border-amber-500/40',
  low: 'bg-destructive/10 text-destructive border-destructive/40',
};

/** One headline figure: its value (or why there is none), what it covers, and its DORA level. */
export function MetricCard({
  title,
  value,
  none,
  detail,
  level,
  note,
  testId,
}: {
  title: string;
  value: string | null;
  none: string;
  detail?: string;
  level?: Level | null;
  note?: string;
  testId?: string;
}) {
  const { t } = useTranslation('dashboard');
  return (
    <Card className="gap-2 p-4" data-testid={testId}>
      <div className="flex flex-wrap items-start justify-between gap-2">
        <h3 className="text-muted-foreground text-sm font-medium">{title}</h3>
        {level && (
          <span
            className={cn('rounded-full border px-2 text-xs leading-5 font-medium', LEVEL_TONE[level])}
            title={t('levels.label')}
            data-level={level}
          >
            {t(`levels.${level}`)}
          </span>
        )}
      </div>
      {value === null ? (
        <p className="text-muted-foreground text-sm">{none}</p>
      ) : (
        <p className="text-2xl font-semibold tabular-nums" data-testid={testId && `${testId}-value`}>
          {value}
        </p>
      )}
      {value !== null && detail && <p className="text-muted-foreground text-xs">{detail}</p>}
      {note && <p className="text-destructive text-xs">{note}</p>}
    </Card>
  );
}
