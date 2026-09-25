import { useQueryClient } from '@tanstack/react-query';
import { RotateCcw, ShieldCheck, X } from 'lucide-react';
import { useId, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';
import { ErrorState, LoadingState } from '@/components/common/States';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { Label } from '@/components/ui/label';
import { canProject, useCurrentProject } from '@/features/project/context';
import { useFormat } from '@/i18n/useFormat';
import { errorMessage } from '@/lib/api/errors';
import { ProjectAction, type ApprovalState, type JobDetail } from '@/lib/api/generated/model';
import { decideJob, getGetJobQueryKey, getGetRunQueryKey, retryJob, useGetJob } from '@/lib/api/generated/pipelines/pipelines';
import { idempotencyHeaders, newIdempotencyKey } from '@/lib/api/idempotency';
import { cn } from '@/lib/utils';
import { useLogStream } from './live';
import { LogViewer } from './LogViewer';
import { useElapsed } from './elapsed';
import { JobStatusLabel, StatusIcon } from './status';

function ApprovalCard({ job, approval }: { job: JobDetail; approval: ApprovalState }) {
  const { t } = useTranslation(['pipeline', 'common']);
  const queryClient = useQueryClient();
  const commentId = useId();
  const [comment, setComment] = useState('');
  const [busy, setBusy] = useState(false);
  const waiting = job.status === 'waiting_approval';

  const decide = async (decision: 'approved' | 'rejected') => {
    setBusy(true);
    try {
      await decideJob(job.id, { decision, comment: comment.trim() });
      toast.success(decision === 'approved' ? t('approval.approved') : t('approval.rejected'));
      setComment('');
    } catch (err) {
      toast.error(errorMessage(err));
    } finally {
      setBusy(false);
    }
    await queryClient.invalidateQueries({ queryKey: getGetJobQueryKey(job.id) });
    await queryClient.invalidateQueries({ queryKey: getGetRunQueryKey(job.run_id) });
  };

  return (
    <div className={cn('space-y-3 rounded-lg border p-4', waiting && 'border-amber-500/50 bg-amber-500/5')} data-testid="approval-card">
      <p className="flex items-center gap-2 font-medium">
        <ShieldCheck aria-hidden className="size-4" />
        {t('approval.title')} · {t('approval.progress', { approved: approval.approved, required: approval.required })}
      </p>
      {approval.protected && job.environment && (
        <div className="text-muted-foreground space-y-1 text-sm">
          <p>{t('approval.protected', { environment: job.environment })}</p>
          {approval.allowed_roles.length > 0 && (
            <p>{t('approval.allowedRoles', { roles: approval.allowed_roles.map((r) => t(`common:roles.${r}`)).join(', ') })}</p>
          )}
          {approval.allowed_branches.length > 0 && <p>{t('approval.allowedBranches', { branches: approval.allowed_branches.join(', ') })}</p>}
        </div>
      )}
      {approval.approvals.length > 0 && (
        <ul className="space-y-1 text-sm">
          {approval.approvals.map((a) => (
            <li key={a.user_id}>
              {a.decision === 'approved' ? t('approval.decisionApproved', { name: a.user_name }) : t('approval.decisionRejected', { name: a.user_name })}
              {a.comment && <span className="text-muted-foreground"> — {a.comment}</span>}
            </li>
          ))}
        </ul>
      )}
      {approval.can_decide ? (
        <div className="space-y-2">
          <Label htmlFor={commentId}>{t('approval.comment')}</Label>
          <textarea
            id={commentId}
            rows={2}
            maxLength={500}
            value={comment}
            onChange={(e) => {
              setComment(e.target.value);
            }}
            className="border-input bg-background focus-visible:border-ring focus-visible:ring-ring/50 w-full rounded-md border px-3 py-2 text-sm outline-none focus-visible:ring-[3px]"
          />
          <div className="flex flex-wrap gap-2">
            <Button disabled={busy} onClick={() => void decide('approved')}>
              {t('approval.approve')}
            </Button>
            <Button variant="outline" disabled={busy} onClick={() => void decide('rejected')}>
              {t('approval.reject')}
            </Button>
          </div>
        </div>
      ) : (
        waiting &&
        approval.denied_reason && <p className="text-muted-foreground text-sm">{t(`approval.denied.${approval.denied_reason}`)}</p>
      )}
    </div>
  );
}

export function JobPanel({ jobId, onSelect, onClose }: { jobId: string; onSelect: (id: string) => void; onClose: () => void }) {
  const { t } = useTranslation(['pipeline', 'common']);
  const fmt = useFormat();
  const elapsed = useElapsed();
  const queryClient = useQueryClient();
  const project = useCurrentProject();
  const job = useGetJob(jobId);
  const logs = useLogStream(jobId);
  const [key, setKey] = useState(newIdempotencyKey);

  if (job.isPending) return <LoadingState rows={4} />;
  if (job.isError) return <ErrorState error={job.error} onRetry={() => void job.refetch()} />;
  const j = job.data;
  const canRetry = canProject(project, ProjectAction.pipelinetrigger) && (j.status === 'failed' || j.status === 'canceled') && j.attempts[0]?.id === j.id;

  const retry = async () => {
    try {
      const next = await retryJob(j.id, idempotencyHeaders(key));
      setKey(newIdempotencyKey());
      toast.success(t('job.retried'));
      await queryClient.invalidateQueries({ queryKey: getGetRunQueryKey(j.run_id) });
      onSelect(next.id);
    } catch (err) {
      toast.error(errorMessage(err));
    }
  };

  const waitingText =
    j.status === 'queued' ? t('job.waitingForRunner') : j.status === 'created' ? t('job.waitingDependencies') : t('job.noLogs');

  return (
    <Card data-testid="job-panel">
      <CardHeader className="flex flex-row flex-wrap items-start justify-between gap-3 space-y-0">
        <div className="min-w-0 space-y-1">
          <CardTitle className="flex flex-wrap items-center gap-2 font-mono">
            {j.name}
            {j.attempt > 1 && <Badge variant="secondary">{t('job.attempt', { n: j.attempt })}</Badge>}
          </CardTitle>
          <div className="flex flex-wrap items-center gap-3">
            <JobStatusLabel status={j.status} />
            {j.started_at && <span className="text-muted-foreground text-sm">{elapsed(j.started_at, j.finished_at)}</span>}
            {j.exit_code !== null && j.exit_code !== 0 && <span className="text-muted-foreground font-mono text-sm">{t('job.exitCode', { code: j.exit_code })}</span>}
          </div>
          {j.failure_reason && <p className="text-muted-foreground text-sm">{t(`reasons.${j.failure_reason}` as 'reasons.timeout')}</p>}
        </div>
        <div className="flex gap-2">
          {canRetry && (
            <Button variant="outline" size="sm" onClick={() => void retry()}>
              <RotateCcw aria-hidden />
              {t('job.retry')}
            </Button>
          )}
          <Button variant="ghost" size="icon" aria-label={t('job.close')} onClick={onClose}>
            <X aria-hidden />
          </Button>
        </div>
      </CardHeader>
      <CardContent className="space-y-5 pt-4">
        {j.approval && <ApprovalCard job={j} approval={j.approval} />}

        <dl className="grid gap-x-6 gap-y-2 text-sm sm:grid-cols-2">
          {j.image && (
            <div>
              <dt className="text-muted-foreground">{t('job.image')}</dt>
              <dd className="font-mono break-all">{j.image}</dd>
            </div>
          )}
          <div>
            <dt className="text-muted-foreground">{t('job.needs')}</dt>
            <dd className="font-mono">{j.needs.length > 0 ? j.needs.join(', ') : t('job.none')}</dd>
          </div>
          {j.environment && (
            <div>
              <dt className="text-muted-foreground">{t('job.environment')}</dt>
              <dd className="font-mono">{j.environment}</dd>
            </div>
          )}
          {j.runs_on.length > 0 && (
            <div>
              <dt className="text-muted-foreground">{t('job.runsOn')}</dt>
              <dd className="font-mono">{j.runs_on.join(', ')}</dd>
            </div>
          )}
        </dl>

        {j.attempts.length > 1 && (
          <div className="space-y-1">
            <p className="text-sm font-medium">{t('job.attempts')}</p>
            <div className="flex flex-wrap gap-2">
              {j.attempts.map((a) => (
                <Button
                  key={a.id}
                  size="sm"
                  variant={a.id === j.id ? 'secondary' : 'ghost'}
                  aria-current={a.id === j.id}
                  onClick={() => {
                    onSelect(a.id);
                  }}
                >
                  <StatusIcon status={a.status} />
                  {t('job.attempt', { n: a.attempt })}
                </Button>
              ))}
            </div>
          </div>
        )}

        {j.steps.length > 0 && (
          <div className="space-y-2">
            <p className="text-sm font-medium">{t('job.steps')}</p>
            <ol className="divide-y rounded-md border">
              {j.steps.map((s) => (
                <li key={s.index} className="flex items-start gap-3 px-3 py-2 text-sm">
                  <StatusIcon status={s.status === 'pending' ? 'created' : s.status} className="mt-0.5" />
                  <span className="min-w-0 flex-1">
                    <span className="sr-only">{t(`stepStatus.${s.status}`)} · </span>
                    <span className="font-mono break-all">{s.name}</span>
                  </span>
                  {s.started_at && <span className="text-muted-foreground shrink-0 text-xs">{elapsed(s.started_at, s.finished_at)}</span>}
                </li>
              ))}
            </ol>
          </div>
        )}

        <div className="space-y-2">
          <p className="text-sm font-medium">{t('job.logs')}</p>
          <LogViewer
            chunks={logs.chunks}
            jobId={j.id}
            fileName={`run-${String(j.run_number)}-${j.name}-attempt-${String(j.attempt)}.log`}
            emptyText={waitingText}
          />
          {j.finished_at && <p className="text-muted-foreground text-xs">{fmt.dateTime(j.finished_at)}</p>}
        </div>
      </CardContent>
    </Card>
  );
}
