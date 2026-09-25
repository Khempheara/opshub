import { Ban, CheckCircle2, CircleDashed, CircleDot, Clock, Hand, LoaderCircle, SkipForward, XCircle, type LucideIcon } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import type { JobStatus, PipelineProblem, RunStatus } from '@/lib/api/generated/model';
import { cn } from '@/lib/utils';

type AnyStatus = RunStatus | JobStatus;

const visuals: Record<AnyStatus, { icon: LucideIcon; tone: string; spin?: boolean }> = {
  created: { icon: CircleDashed, tone: 'text-muted-foreground' },
  queued: { icon: Clock, tone: 'text-muted-foreground' },
  running: { icon: LoaderCircle, tone: 'text-sky-600 dark:text-sky-400', spin: true },
  waiting: { icon: Hand, tone: 'text-amber-600 dark:text-amber-400' },
  waiting_approval: { icon: Hand, tone: 'text-amber-600 dark:text-amber-400' },
  succeeded: { icon: CheckCircle2, tone: 'text-success' },
  failed: { icon: XCircle, tone: 'text-destructive' },
  canceled: { icon: Ban, tone: 'text-muted-foreground' },
  skipped: { icon: SkipForward, tone: 'text-muted-foreground' },
};

export function StatusIcon({ status, className }: { status: AnyStatus; className?: string }) {
  const v = visuals[status] as (typeof visuals)[AnyStatus] | undefined;
  const Icon = v?.icon ?? CircleDot;
  return <Icon aria-hidden className={cn('size-4 shrink-0', v?.tone ?? 'text-muted-foreground', v?.spin && 'motion-safe:animate-spin', className)} />;
}

/** Icon + label for a run. */
export function RunStatusBadge({ status }: { status: RunStatus }) {
  const { t } = useTranslation('pipeline');
  return (
    <span className="inline-flex items-center gap-1.5 text-sm font-medium" data-status={status}>
      <StatusIcon status={status} />
      {t(`status.${status}`)}
    </span>
  );
}

/** Icon + label for a job. */
export function JobStatusLabel({ status }: { status: JobStatus }) {
  const { t } = useTranslation('pipeline');
  return (
    <span className="inline-flex items-center gap-1.5 text-sm" data-status={status}>
      <StatusIcon status={status} />
      {t(`jobStatus.${status}`)}
    </span>
  );
}

const RULES = new Set([
  'syntax',
  'empty',
  'file_too_large',
  'type',
  'unknown_field',
  'duplicate',
  'required',
  'unsupported_version',
  'oneof',
  'pattern',
  'max',
  'schedule_needs_cron',
  'invalid_cron',
  'variable_name',
  'reserved',
  'job_name',
  'unknown_stage',
  'unknown_job',
  'self_need',
  'need_later_stage',
  'cycle',
  'duration',
  'range',
  'path',
  'deploy_requires_environment',
]);

/** Pipeline file problems with line numbers, translated by rule code. */
export function ProblemList({ problems }: { problems: PipelineProblem[] }) {
  const { t } = useTranslation('pipeline');
  return (
    <ul className="space-y-2" data-testid="pipeline-problems">
      {problems.map((p, i) => (
        <li key={`${String(p.line)}-${String(p.column)}-${String(i)}`} className="border-destructive/30 bg-destructive/5 rounded-md border px-3 py-2 text-sm">
          <p className="font-medium">
            {RULES.has(p.rule) ? t(`rules.${p.rule}` as 'rules.required', { param: p.param ?? '' }) : t('rules.default', { rule: p.rule })}
          </p>
          <p className="text-muted-foreground font-mono text-xs">
            {t('problem.location', { line: p.line, column: p.column })}
            {p.path && ` · ${t('problem.path', { path: p.path })}`}
          </p>
        </li>
      ))}
    </ul>
  );
}
