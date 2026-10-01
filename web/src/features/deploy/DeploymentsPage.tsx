import { useQueryClient } from '@tanstack/react-query';
import { Rocket } from 'lucide-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useNavigate } from 'react-router';
import { toast } from 'sonner';
import { useCurrentOrg } from '@/app/org';
import { Field, TextField } from '@/components/common/Field';
import { EmptyState, ErrorState, FormError, LoadingState } from '@/components/common/States';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from '@/components/ui/dialog';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table';
import { selectClass } from '@/features/org/constants';
import { canProject, useCurrentProject } from '@/features/project/context';
import { useFormat } from '@/i18n/useFormat';
import { fieldErrorMessage, hasCode } from '@/lib/api/errors';
import { ApiError } from '@/lib/api/fetcher';
import { DeploymentStatus, ProjectAction, type DeployStrategy, type Deployment } from '@/lib/api/generated/model';
import { createDeployment, getListDeploymentsQueryKey, useListDeployTargets, useListDeployments } from '@/lib/api/generated/deployments/deployments';
import { useListEnvironments } from '@/lib/api/generated/environments/environments';
import { idempotencyHeaders, newIdempotencyKey } from '@/lib/api/idempotency';
import { DeploymentStatusLabel } from './status';

const STATUSES = Object.values(DeploymentStatus);
const VERSION_EXAMPLE = 'ghcr.io/acme/api:1.4.2';

function DeployDialog({ projectId, orgId, base }: { projectId: string; orgId: string; base: string }) {
  const { t } = useTranslation(['deploy', 'common']);
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const envs = useListEnvironments(projectId);
  const targets = useListDeployTargets(orgId);
  const [open, setOpen] = useState(false);
  const [envId, setEnvId] = useState('');
  const [targetId, setTargetId] = useState('');
  const [version, setVersion] = useState('');
  const [strategy, setStrategy] = useState<DeployStrategy>('rolling');
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const [key, setKey] = useState(newIdempotencyKey);
  const target = targets.data?.items.find((x) => x.id === targetId);
  const versionError = error instanceof ApiError ? error.fieldErrors.find((f) => f.field === 'version') : undefined;

  const submit = async () => {
    setBusy(true);
    setError(null);
    try {
      const d = await createDeployment(envId, { target_id: targetId, version: version.trim(), strategy }, idempotencyHeaders(key));
      toast.success(t('deploy.started', { number: d.number }));
      await queryClient.invalidateQueries({ queryKey: getListDeploymentsQueryKey(projectId) });
      setOpen(false);
      await navigate(`${base}/${d.id}`);
    } catch (err) {
      setError(err);
      if (!hasCode(err, 'IDEMPOTENCY_KEY_IN_PROGRESS')) setKey(newIdempotencyKey());
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        setOpen(o);
        if (o) {
          setError(null);
          setKey(newIdempotencyKey());
        }
      }}
    >
      <DialogTrigger asChild>
        <Button>
          <Rocket aria-hidden />
          {t('deploy.button')}
        </Button>
      </DialogTrigger>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('deploy.title')}</DialogTitle>
          <DialogDescription>{t('deploy.description')}</DialogDescription>
        </DialogHeader>
        <form
          noValidate
          className="space-y-4"
          onSubmit={(e) => {
            e.preventDefault();
            void submit();
          }}
        >
          <Field label={t('deploy.environment')}>
            {({ id }) => (
              <select
                id={id}
                className={selectClass}
                value={envId}
                onChange={(e) => {
                  setEnvId(e.target.value);
                }}
              >
                <option value="">{t('deploy.choose')}</option>
                {envs.data?.items.map((env) => (
                  <option key={env.id} value={env.id}>
                    {env.name}
                    {env.protection ? ` · ${t('deploy.protected')}` : ''}
                  </option>
                ))}
              </select>
            )}
          </Field>
          <Field label={t('deploy.target')} hint={targets.data?.items.length === 0 ? t('deploy.noTargets') : undefined}>
            {({ id, describedBy }) => (
              <select
                id={id}
                aria-describedby={describedBy}
                className={selectClass}
                value={targetId}
                onChange={(e) => {
                  setTargetId(e.target.value);
                  setStrategy('rolling');
                }}
              >
                <option value="">{t('deploy.choose')}</option>
                {targets.data?.items.map((tg) => (
                  <option key={tg.id} value={tg.id}>
                    {tg.name} ({t(`kinds.${tg.kind}`)})
                  </option>
                ))}
              </select>
            )}
          </Field>
          <TextField
            label={t('deploy.version')}
            hint={t('deploy.versionHint')}
            placeholder={VERSION_EXAMPLE}
            dir="ltr"
            autoComplete="off"
            value={version}
            error={versionError ? fieldErrorMessage(versionError) : undefined}
            onChange={(e) => {
              setVersion(e.target.value);
            }}
          />
          <Field label={t('deploy.strategy')} hint={target && target.kind !== 'kubernetes' ? t('deploy.blueGreenK8sOnly') : undefined}>
            {({ id, describedBy }) => (
              <select
                id={id}
                aria-describedby={describedBy}
                className={selectClass}
                value={strategy}
                onChange={(e) => {
                  setStrategy(e.target.value as DeployStrategy);
                }}
              >
                <option value="rolling">{t('strategies.rolling')}</option>
                <option value="blue_green" disabled={target?.kind !== 'kubernetes'}>
                  {t('strategies.blue_green')}
                </option>
              </select>
            )}
          </Field>
          {!versionError && <FormError error={error} />}
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => {
                setOpen(false);
              }}
            >
              {t('common:actions.cancel')}
            </Button>
            <Button type="submit" disabled={busy || !envId || !targetId || !version.trim()}>
              {t('deploy.submit')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function VersionText({ d }: { d: Deployment }) {
  return (
    <span className="font-mono text-xs break-all" dir="ltr">
      {d.version}
    </span>
  );
}

/** Project → Deployments: what each environment runs, and the release history. */
export function DeploymentsPage() {
  const { t } = useTranslation(['deploy', 'common']);
  const fmt = useFormat();
  const org = useCurrentOrg();
  const project = useCurrentProject();
  const base = `/o/${org.slug}/projects/${project.id}/deployments`;
  const [envFilter, setEnvFilter] = useState('');
  const [statusFilter, setStatusFilter] = useState('');
  const current = useListDeployments(project.id, { current: true, limit: 50 }, { query: { refetchInterval: 15_000 } });
  const history = useListDeployments(
    project.id,
    { environment_id: envFilter || undefined, status: (statusFilter || undefined) as DeploymentStatus | undefined, limit: 50 },
    { query: { refetchInterval: 10_000 } },
  );
  const envs = useListEnvironments(project.id);
  const canDeploy = canProject(project, ProjectAction.deploymentcreate);

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h2 className="text-lg font-semibold">{t('tab')}</h2>
          <p className="text-muted-foreground text-sm">{t('description')}</p>
        </div>
        {canDeploy && <DeployDialog projectId={project.id} orgId={org.id} base={base} />}
      </div>

      <section className="space-y-2" aria-labelledby="current-heading">
        <h3 id="current-heading" className="text-sm font-semibold">
          {t('current.title')}
        </h3>
        {current.isPending ? (
          <LoadingState rows={1} />
        ) : current.isError ? (
          <ErrorState error={current.error} onRetry={() => void current.refetch()} />
        ) : current.data.items.length === 0 ? (
          <p className="text-muted-foreground text-sm">{t('current.none')}</p>
        ) : (
          <ul className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3" data-testid="current-releases">
            {current.data.items.map((d) => (
              <li key={d.id}>
                <Card className="h-full gap-2 py-4">
                  <CardHeader className="px-4">
                    <CardTitle className="font-mono text-sm">{d.environment_name}</CardTitle>
                  </CardHeader>
                  <CardContent className="space-y-1 px-4 text-sm">
                    <Link to={`${base}/${d.id}`} className="hover:underline">
                      <VersionText d={d} />
                    </Link>
                    <p className="text-muted-foreground text-xs">
                      {t('current.on', { target: d.target_name })} · {d.finished_at ? fmt.relative(d.finished_at) : ''}
                    </p>
                  </CardContent>
                </Card>
              </li>
            ))}
          </ul>
        )}
      </section>

      <section className="space-y-3" aria-labelledby="history-heading">
        <div className="flex flex-wrap items-end justify-between gap-3">
          <h3 id="history-heading" className="text-sm font-semibold">
            {t('history.title')}
          </h3>
          <div className="flex flex-wrap gap-2">
            <label className="sr-only" htmlFor="dep-env">
              {t('history.environment')}
            </label>
            <select
              id="dep-env"
              className={selectClass}
              value={envFilter}
              onChange={(e) => {
                setEnvFilter(e.target.value);
              }}
            >
              <option value="">{t('history.allEnvironments')}</option>
              {envs.data?.items.map((env) => (
                <option key={env.id} value={env.id}>
                  {env.name}
                </option>
              ))}
            </select>
            <label className="sr-only" htmlFor="dep-status">
              {t('history.status')}
            </label>
            <select
              id="dep-status"
              className={selectClass}
              value={statusFilter}
              onChange={(e) => {
                setStatusFilter(e.target.value);
              }}
            >
              <option value="">{t('history.allStatuses')}</option>
              {STATUSES.map((s) => (
                <option key={s} value={s}>
                  {t(`status.${s}`)}
                </option>
              ))}
            </select>
          </div>
        </div>
        {history.isPending ? (
          <LoadingState />
        ) : history.isError ? (
          <ErrorState error={history.error} onRetry={() => void history.refetch()} />
        ) : history.data.items.length === 0 ? (
          <EmptyState title={t('history.empty')} description={canDeploy ? t('history.emptyHint') : undefined} />
        ) : (
          <Card className="gap-0 overflow-x-auto p-0">
            <Table data-testid="deployments-table">
              <TableHeader>
                <TableRow>
                  <TableHead>{t('history.columns.number')}</TableHead>
                  <TableHead>{t('history.columns.environment')}</TableHead>
                  <TableHead>{t('history.columns.version')}</TableHead>
                  <TableHead>{t('history.columns.status')}</TableHead>
                  <TableHead>{t('history.columns.target')}</TableHead>
                  <TableHead>{t('history.columns.by')}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {history.data.items.map((d) => (
                  <TableRow key={d.id} data-testid="deployment-row">
                    <TableCell>
                      <Link to={`${base}/${d.id}`} className="font-medium hover:underline">
                        #{fmt.number(d.number)}
                      </Link>
                      {d.rollback_of_id && <span className="text-muted-foreground block text-xs">{t('history.rollback')}</span>}
                    </TableCell>
                    <TableCell className="font-mono text-xs">{d.environment_name}</TableCell>
                    <TableCell className="max-w-xs">
                      <VersionText d={d} />
                      {d.current && <span className="text-success block text-xs font-medium">{t('history.live')}</span>}
                    </TableCell>
                    <TableCell className="whitespace-nowrap">
                      <DeploymentStatusLabel deployment={d} />
                    </TableCell>
                    <TableCell className="text-sm whitespace-nowrap">
                      {d.target_name} · {t(`strategies.${d.strategy}`)}
                    </TableCell>
                    <TableCell className="text-sm">
                      {d.run_number
                        ? t(d.job_name ? 'history.byRun' : 'history.byRunOnly', { number: fmt.number(d.run_number), job: d.job_name })
                        : d.created_by_name || t('history.system')}
                      <span className="text-muted-foreground block text-xs">{fmt.relative(d.created_at)}</span>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </Card>
        )}
      </section>
    </div>
  );
}
