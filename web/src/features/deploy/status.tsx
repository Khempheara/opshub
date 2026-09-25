import { CheckCircle2, Clock, LoaderCircle, Undo2, XCircle, type LucideIcon } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import type { Deployment, DeploymentStatus } from '@/lib/api/generated/model';
import { cn } from '@/lib/utils';

const visuals: Record<DeploymentStatus, { icon: LucideIcon; tone: string; spin?: boolean }> = {
  pending: { icon: Clock, tone: 'text-muted-foreground' },
  running: { icon: LoaderCircle, tone: 'text-sky-600 dark:text-sky-400', spin: true },
  succeeded: { icon: CheckCircle2, tone: 'text-success' },
  failed: { icon: XCircle, tone: 'text-destructive' },
};

/** Icon + label for a deployment's status (and whether a failure was reverted). */
export function DeploymentStatusLabel({ deployment }: { deployment: Pick<Deployment, 'status' | 'reverted'> }) {
  const { t } = useTranslation('deploy');
  const v = visuals[deployment.status];
  return (
    <span className="inline-flex items-center gap-1.5 text-sm" data-status={deployment.status}>
      <v.icon aria-hidden className={cn('size-4 shrink-0', v.tone, v.spin && 'motion-safe:animate-spin')} />
      {t(`status.${deployment.status}`)}
      {deployment.reverted && (
        <span className="text-muted-foreground inline-flex items-center gap-1">
          · <Undo2 aria-hidden className="size-3.5" />
          {t('reverted')}
        </span>
      )}
    </span>
  );
}

const REASONS = new Set([
  'deploy_failed',
  'health_check_failed',
  'target_unreachable',
  'host_key_untrusted',
  'target_missing',
  'interrupted',
  'strategy_not_supported',
]);

/** A failed deployment's reason, translated. */
export function FailureReason({ reason }: { reason: string }) {
  const { t } = useTranslation('deploy');
  return <>{REASONS.has(reason) ? t(`reasons.${reason}` as 'reasons.deploy_failed') : reason}</>;
}
