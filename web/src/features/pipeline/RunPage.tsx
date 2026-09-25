import { useQueryClient } from '@tanstack/react-query';
import { ArrowLeft, GitBranch, GitCommitHorizontal, Radio, RotateCcw } from 'lucide-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useNavigate, useParams, useSearchParams } from 'react-router';
import { toast } from 'sonner';
import { useCurrentOrg } from '@/app/org';
import { ConfirmDialog } from '@/components/common/ConfirmDialog';
import { ErrorState, LoadingState } from '@/components/common/States';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { canProject, useCurrentProject } from '@/features/project/context';
import { useFormat } from '@/i18n/useFormat';
import { errorMessage, hasCode } from '@/lib/api/errors';
import { ProjectAction, type Job, type RunDetail } from '@/lib/api/generated/model';
import { cancelRun, getGetRunQueryKey, getListRunsQueryKey, rerunRun, useGetRun } from '@/lib/api/generated/pipelines/pipelines';
import { idempotencyHeaders, newIdempotencyKey } from '@/lib/api/idempotency';
import { cn } from '@/lib/utils';
import { NotFoundPage } from '@/pages/NotFoundPage';
import { JobPanel } from './JobPanel';
import { useLiveRun } from './live';
import { useElapsed } from './elapsed';
import { ProblemList, RunStatusBadge, StatusIcon } from './status';

const ACTIVE = new Set(['queued', 'running', 'waiting']);

function JobCard({ job, selected, onSelect }: { job: Job; selected: boolean; onSelect: () => void }) {
  const { t } = useTranslation('pipeline');
  const elapsed = useElapsed();
  return (
    <button
      type="button"
      onClick={onSelect}
      aria-pressed={selected}
      aria-label={t('job.open', { name: job.name })}
      data-testid={`job-${job.name}`}
      className={cn(
        'hover:bg-accent/50 focus-visible:ring-ring/50 flex w-full items-center gap-2 rounded-md border px-3 py-2 text-start text-sm outline-none focus-visible:ring-[3px]',
        selected && 'border-primary bg-accent/40',
      )}
    >
      <StatusIcon status={job.status} />
      <span className="min-w-0 flex-1">
        <span className="block truncate font-mono">{job.name}</span>
        <span className="text-muted-foreground block truncate text-xs">
          {t(`jobStatus.${job.status}`)}
          {job.environment && ` · ${job.environment}`}
          {job.condition === 'manual' && ` · ${t('job.manual')}`}
        </span>
      </span>
      {job.started_at && <span className="text-muted-foreground shrink-0 text-xs">{elapsed(job.started_at, job.finished_at)}</span>}
    </button>
  );
}

function Actions({ run }: { run: RunDetail }) {
  const { t } = useTranslation('pipeline');
  const org = useCurrentOrg();
  const project = useCurrentProject();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [key, setKey] = useState(newIdempotencyKey);
  const active = ACTIVE.has(run.status);
  const canCancel = canProject(project, ProjectAction.runcancel) && active;
  const canRerun = canProject(project, ProjectAction.pipelinetrigger) && !active && run.problems.length === 0;
  const hasFailed = run.jobs.some((j) => j.status === 'failed' || j.status === 'canceled');

  const refresh = async () => {
    await queryClient.invalidateQueries({ queryKey: getGetRunQueryKey(run.id) });
    await queryClient.invalidateQueries({ queryKey: getListRunsQueryKey(project.id) });
  };
  const rerun = async (failedOnly: boolean) => {
    try {
      const next = await rerunRun(run.id, { failed_only: failedOnly }, idempotencyHeaders(key));
      setKey(newIdempotencyKey());
      await refresh();
      if (failedOnly) {
        toast.success(t('run.retried'));
      } else {
        toast.success(t('run.rerunStarted', { number: next.number }));
        await navigate(`/o/${org.slug}/projects/${project.id}/runs/${next.id}`);
      }
    } catch (err) {
      toast.error(errorMessage(err));
    }
  };

  return (
    <div className="flex flex-wrap gap-2">
      {canCancel && (
        <ConfirmDialog
          trigger={<Button variant="outline">{t('run.cancel')}</Button>}
          title={t('run.cancelTitle', { number: run.number })}
          description={t('run.cancelBody')}
          confirmLabel={t('run.cancel')}
          onConfirm={async () => {
            try {
              await cancelRun(run.id);
              toast.success(t('run.canceled'));
            } catch (err) {
              toast.error(errorMessage(err));
            }
            await refresh();
          }}
        />
      )}
      {canRerun && hasFailed && (
        <Button variant="outline" onClick={() => void rerun(true)}>
          <RotateCcw aria-hidden />
          {t('run.rerunFailed')}
        </Button>
      )}
      {canRerun && (
        <Button variant="outline" onClick={() => void rerun(false)}>
          <RotateCcw aria-hidden />
          {t('run.rerun')}
        </Button>
      )}
    </div>
  );
}

export function RunPage() {
  const { t } = useTranslation('pipeline');
  const { runId = '' } = useParams();
  const org = useCurrentOrg();
  const project = useCurrentProject();
  const fmt = useFormat();
  const elapsed = useElapsed();
  const [params, setParams] = useSearchParams();
  const selected = params.get('job') ?? undefined;
  const run = useGetRun(runId, { query: { retry: (n, err) => !hasCode(err, 'RUN_NOT_FOUND') && n < 2 } });
  const live = useLiveRun(runId, project.id, selected);

  if (run.isPending) return <LoadingState rows={4} />;
  if (run.isError) {
    if (hasCode(run.error, 'RUN_NOT_FOUND')) return <NotFoundPage />;
    return <ErrorState error={run.error} onRetry={() => void run.refetch()} />;
  }
  const r = run.data;
  if (r.project_id !== project.id) return <NotFoundPage />;

  const select = (id?: string) => {
    const next = new URLSearchParams(params);
    if (id) next.set('job', id);
    else next.delete('job');
    setParams(next, { replace: true });
  };
  const stages = r.stages.length > 0 ? r.stages : [...new Set(r.jobs.map((j) => j.stage))];
  const who = r.created_by_name ? t('run.triggeredBy', { name: r.created_by_name }) : r.actor_name ? t('run.pushedBy', { name: r.actor_name }) : '';
  const vars = Object.entries(r.variables);

  return (
    <section className="space-y-5" aria-labelledby="run-title">
      <Link to={`/o/${org.slug}/projects/${project.id}`} className="text-muted-foreground hover:text-foreground inline-flex items-center gap-1 text-sm">
        <ArrowLeft aria-hidden className="size-4 rtl:rotate-180" />
        {t('run.back')}
      </Link>
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0 space-y-1">
          <h2 id="run-title" className="flex flex-wrap items-center gap-3 text-xl font-semibold">
            {t('run.title', { number: fmt.number(r.number) })}
            <RunStatusBadge status={r.status} />
            {live && ACTIVE.has(r.status) && (
              <span className="text-muted-foreground inline-flex items-center gap-1 text-xs font-normal">
                <Radio aria-hidden className="size-3 text-sky-500" />
                {t('run.live')}
              </span>
            )}
          </h2>
          {r.title && <p className="break-words">{r.title}</p>}
          <p className="text-muted-foreground flex flex-wrap items-center gap-x-4 gap-y-1 text-sm">
            <span className="inline-flex items-center gap-1 font-mono">
              <GitBranch aria-hidden className="size-4" />
              {r.ref_name}
            </span>
            <span className="inline-flex items-center gap-1 font-mono" title={r.commit_sha}>
              <GitCommitHorizontal aria-hidden className="size-4" />
              {r.commit_sha.slice(0, 7)}
            </span>
            <span>{t(`triggers.${r.trigger}`)}</span>
            {who && <span>{who}</span>}
            {r.started_at && <span>{elapsed(r.started_at, r.finished_at)}</span>}
            <span>{fmt.relative(r.created_at)}</span>
          </p>
        </div>
        <Actions run={r} />
      </div>

      {r.problems.length > 0 && (
        <Card className="border-destructive/40">
          <CardHeader>
            <CardTitle className="text-destructive">{t('run.invalidTitle')}</CardTitle>
          </CardHeader>
          <CardContent className="space-y-3 pt-4">
            <p className="text-sm">{t('run.invalidBody')}</p>
            <ProblemList problems={r.problems} />
          </CardContent>
        </Card>
      )}

      {vars.length > 0 && (
        <p className="text-muted-foreground text-sm">
          {t('run.variablesList', { list: vars.map(([k, v]) => `${k}=${v}`).join(' ') })}
        </p>
      )}

      {r.jobs.length > 0 && (
        <div className="flex gap-4 overflow-x-auto pb-2" data-testid="run-graph">
          {stages.map((stage) => (
            <div key={stage} className="w-60 shrink-0 space-y-2">
              <p className="text-muted-foreground text-xs font-medium tracking-wide uppercase">{t('run.stage', { name: stage })}</p>
              {r.jobs
                .filter((j) => j.stage === stage)
                .map((j) => (
                  <JobCard
                    key={j.id}
                    job={j}
                    selected={selected === j.id}
                    onSelect={() => {
                      select(j.id);
                    }}
                  />
                ))}
            </div>
          ))}
        </div>
      )}

      {selected && (
        <JobPanel
          key={selected}
          jobId={selected}
          onSelect={select}
          onClose={() => {
            select();
          }}
        />
      )}
    </section>
  );
}
