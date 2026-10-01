import { useQueryClient } from '@tanstack/react-query';
import { ArrowLeft, CheckCircle2, Undo2, XCircle } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { Link, useNavigate, useParams } from 'react-router';
import { toast } from 'sonner';
import { useCurrentOrg } from '@/app/org';
import { ConfirmDialog } from '@/components/common/ConfirmDialog';
import { ErrorState, LoadingState } from '@/components/common/States';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { useElapsed } from '@/features/pipeline/elapsed';
import { LogViewer } from '@/features/pipeline/LogViewer';
import { canProject, useCurrentProject } from '@/features/project/context';
import { useFormat } from '@/i18n/useFormat';
import { errorMessage, hasCode } from '@/lib/api/errors';
import { ProjectAction } from '@/lib/api/generated/model';
import { getListDeploymentsQueryKey, rollbackDeployment, useGetDeployment } from '@/lib/api/generated/deployments/deployments';
import { idempotencyHeaders, newIdempotencyKey } from '@/lib/api/idempotency';
import { NotFoundPage } from '@/pages/NotFoundPage';
import { useDeploymentStream } from './live';
import { DeploymentStatusLabel, FailureReason } from './status';

/** One deployment: facts, health checks, live log and rollback. */
export function DeploymentPage() {
  const { t } = useTranslation(['deploy', 'common']);
  const fmt = useFormat();
  const elapsed = useElapsed();
  const { deploymentId = '' } = useParams();
  const org = useCurrentOrg();
  const project = useCurrentProject();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const base = `/o/${org.slug}/projects/${project.id}/deployments`;
  const dep = useGetDeployment(deploymentId, { query: { retry: (n, err) => !hasCode(err, 'DEPLOYMENT_NOT_FOUND') && n < 2 } });
  const logs = useDeploymentStream(deploymentId, project.id);

  if (dep.isPending) return <LoadingState />;
  if (dep.isError) {
    if (hasCode(dep.error, 'DEPLOYMENT_NOT_FOUND')) return <NotFoundPage />;
    return <ErrorState error={dep.error} onRetry={() => void dep.refetch()} />;
  }
  const d = dep.data;
  if (d.project_id !== project.id) return <NotFoundPage />;
  const canRollback = canProject(project, ProjectAction.deploymentrollback) && d.current && d.status === 'succeeded';
  const runLink = d.run_id ? `/o/${org.slug}/projects/${project.id}/runs/${d.run_id}` : null;

  const rollback = async () => {
    try {
      const rb = await rollbackDeployment(d.id, idempotencyHeaders(newIdempotencyKey()));
      toast.success(t('rollback.started', { number: rb.number }));
      await queryClient.invalidateQueries({ queryKey: getListDeploymentsQueryKey(project.id) });
      await navigate(`${base}/${rb.id}`);
    } catch (err) {
      toast.error(errorMessage(err));
    }
  };

  const facts: [string, React.ReactNode][] = [
    [t('detail.environment'), <span className="font-mono">{d.environment_name}</span>],
    [
      t('detail.version'),
      <span className="font-mono break-all" dir="ltr">
        {d.version}
      </span>,
    ],
    [
      t('detail.previous'),
      d.previous_version ? (
        <span className="font-mono break-all" dir="ltr">
          {d.previous_version}
        </span>
      ) : (
        t('detail.none')
      ),
    ],
    [t('detail.target'), `${d.target_name} (${t(`kinds.${d.target_kind}`)}) · ${t(`strategies.${d.strategy}`)}`],
    [
      t('detail.startedBy'),
      runLink ? (
        <Link to={runLink} className="hover:underline">
          {t(d.job_name ? 'history.byRun' : 'history.byRunOnly', { number: fmt.number(d.run_number ?? 0), job: d.job_name })}
        </Link>
      ) : (
        d.created_by_name || t('history.system')
      ),
    ],
    [t('detail.created'), fmt.dateTime(d.created_at)],
  ];
  if (d.started_at) facts.push([t('detail.duration'), elapsed(d.started_at, d.finished_at)]);

  return (
    <div className="space-y-5">
      <Link to={base} className="text-muted-foreground hover:text-foreground inline-flex items-center gap-1 text-sm">
        <ArrowLeft aria-hidden className="size-4 rtl:rotate-180" />
        {t('detail.back')}
      </Link>
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="space-y-1">
          <h2 className="text-lg font-semibold">
            {d.rollback_of_id ? t('detail.rollbackTitle', { number: fmt.number(d.number) }) : t('detail.title', { number: fmt.number(d.number) })}
          </h2>
          <div className="flex flex-wrap items-center gap-3" data-testid="deployment-status">
            <DeploymentStatusLabel deployment={d} />
            {d.current && <span className="text-success text-sm font-medium">{t('history.live')}</span>}
          </div>
          {d.failure_reason && (
            <p className="text-muted-foreground text-sm">
              <FailureReason reason={d.failure_reason} />
              {d.reverted && ` ${t('detail.revertedNote')}`}
            </p>
          )}
        </div>
        {canRollback && (
          <ConfirmDialog
            trigger={
              <Button variant="outline">
                <Undo2 aria-hidden />
                {t('rollback.button')}
              </Button>
            }
            title={t('rollback.title', { environment: d.environment_name })}
            description={t('rollback.body', { current: d.version, previous: d.previous_version || t('detail.none') })}
            confirmLabel={t('rollback.confirm')}
            onConfirm={rollback}
          />
        )}
      </div>

      <Card>
        <CardContent className="pt-0">
          <dl className="grid gap-x-6 gap-y-3 text-sm sm:grid-cols-2">
            {facts.map(([label, value]) => (
              <div key={label} className="min-w-0">
                <dt className="text-muted-foreground">{label}</dt>
                <dd>{value}</dd>
              </div>
            ))}
          </dl>
        </CardContent>
      </Card>

      {d.health.length > 0 && (
        <Card>
          <CardHeader>
            <CardTitle className="text-base">{t('detail.health')}</CardTitle>
          </CardHeader>
          <CardContent>
            <ul className="divide-y rounded-md border" data-testid="health-results">
              {d.health.map((h, i) => (
                <li key={i} className="flex items-start gap-2 px-3 py-2 text-sm">
                  {h.ok ? <CheckCircle2 aria-hidden className="text-success mt-0.5 size-4 shrink-0" /> : <XCircle aria-hidden className="text-destructive mt-0.5 size-4 shrink-0" />}
                  <span className="min-w-0 flex-1">
                    <span className="font-mono break-all" dir="ltr">
                      {h.target}
                    </span>
                    {h.detail && <span className="text-muted-foreground block">{h.detail}</span>}
                  </span>
                  <span className="text-muted-foreground shrink-0 text-xs">{t('detail.attempts', { count: h.attempts })}</span>
                </li>
              ))}
            </ul>
          </CardContent>
        </Card>
      )}

      <div className="space-y-2">
        <h3 className="text-sm font-semibold">{t('detail.log')}</h3>
        <LogViewer
          chunks={logs.chunks}
          fileName={`deployment-${String(d.number)}-${d.environment_name}.log`}
          emptyText={d.status === 'pending' ? t('detail.waiting') : t('detail.noLog')}
        />
      </div>
    </div>
  );
}
