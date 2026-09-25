import { useQueryClient } from '@tanstack/react-query';
import { FileCheck2, GitBranch, GitCommitHorizontal, Play, Plus, X } from 'lucide-react';
import { useId, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useNavigate } from 'react-router';
import { toast } from 'sonner';
import { useCurrentOrg } from '@/app/org';
import { EmptyState, ErrorState, FormError, LoadingState } from '@/components/common/States';
import { Button } from '@/components/ui/button';
import { Card } from '@/components/ui/card';
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from '@/components/ui/dialog';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table';
import { selectClass } from '@/features/org/constants';
import { canProject, useCurrentProject } from '@/features/project/context';
import { useFormat } from '@/i18n/useFormat';
import { hasCode } from '@/lib/api/errors';
import { ProjectAction, RunStatus, RunTrigger, type PipelineProblem, type Run } from '@/lib/api/generated/model';
import { getListRunsQueryKey, triggerRun, useListRuns, validatePipeline } from '@/lib/api/generated/pipelines/pipelines';
import { useGetRepository } from '@/lib/api/generated/repositories/repositories';
import { idempotencyHeaders, newIdempotencyKey } from '@/lib/api/idempotency';
import { cn } from '@/lib/utils';
import { useElapsed } from './elapsed';
import { ProblemList, RunStatusBadge } from './status';

const STATUSES = Object.values(RunStatus);
const TRIGGERS = Object.values(RunTrigger);

interface VarRow {
  key: string;
  value: string;
}

function TriggerDialog({ projectId, defaultBranch, runBase }: { projectId: string; defaultBranch: string; runBase: string }) {
  const { t } = useTranslation(['pipeline', 'common']);
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const refId = useId();
  const [open, setOpen] = useState(false);
  const [ref, setRef] = useState('');
  const [vars, setVars] = useState<VarRow[]>([]);
  const [problems, setProblems] = useState<PipelineProblem[]>([]);
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const [key, setKey] = useState(newIdempotencyKey);

  const submit = async () => {
    setBusy(true);
    setError(null);
    setProblems([]);
    try {
      const variables = Object.fromEntries(vars.filter((v) => v.key.trim()).map((v) => [v.key.trim(), v.value]));
      const run = await triggerRun(projectId, { ref: ref.trim(), variables }, idempotencyHeaders(key));
      toast.success(t('trigger.started', { number: run.number }));
      await queryClient.invalidateQueries({ queryKey: getListRunsQueryKey(projectId) });
      setOpen(false);
      await navigate(`${runBase}/${run.id}`);
    } catch (err) {
      if (hasCode(err, 'PIPELINE_INVALID')) {
        setProblems((err.details.problems as PipelineProblem[] | undefined) ?? []);
      } else {
        setError(err);
      }
      setKey(newIdempotencyKey()); // a changed request needs a new key
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (next) {
          setRef('');
          setVars([]);
          setProblems([]);
          setError(null);
          setKey(newIdempotencyKey());
        }
      }}
    >
      <DialogTrigger asChild>
        <Button>
          <Play aria-hidden />
          {t('runs.run')}
        </Button>
      </DialogTrigger>
      <DialogContent className="max-h-[90dvh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>{t('trigger.title')}</DialogTitle>
        </DialogHeader>
        <form
          noValidate
          className="space-y-4"
          onSubmit={(e) => {
            e.preventDefault();
            void submit();
          }}
        >
          <div className="space-y-1.5">
            <Label htmlFor={refId}>{t('trigger.ref')}</Label>
            <Input
              id={refId}
              value={ref}
              placeholder={defaultBranch}
              autoComplete="off"
              spellCheck={false}
              className="font-mono"
              onChange={(e) => {
                setRef(e.target.value);
              }}
            />
            <p className="text-muted-foreground text-xs">{t('trigger.refHint', { branch: defaultBranch })}</p>
          </div>
          <fieldset className="space-y-2">
            <legend className="text-sm font-medium">{t('trigger.variables')}</legend>
            <p className="text-muted-foreground text-xs">{t('trigger.variablesHint')}</p>
            {vars.map((v, i) => (
              <div key={i} className="flex gap-2">
                <Input
                  aria-label={t('trigger.variableName')}
                  placeholder={t('trigger.variableName')}
                  className="font-mono"
                  value={v.key}
                  onChange={(e) => {
                    setVars(vars.map((x, j) => (j === i ? { ...x, key: e.target.value } : x)));
                  }}
                />
                <Input
                  aria-label={t('trigger.variableValue')}
                  placeholder={t('trigger.variableValue')}
                  className="font-mono"
                  value={v.value}
                  onChange={(e) => {
                    setVars(vars.map((x, j) => (j === i ? { ...x, value: e.target.value } : x)));
                  }}
                />
                <Button
                  type="button"
                  variant="ghost"
                  size="icon"
                  aria-label={t('trigger.removeVariable')}
                  onClick={() => {
                    setVars(vars.filter((_, j) => j !== i));
                  }}
                >
                  <X aria-hidden />
                </Button>
              </div>
            ))}
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => {
                setVars([...vars, { key: '', value: '' }]);
              }}
            >
              <Plus aria-hidden />
              {t('trigger.addVariable')}
            </Button>
          </fieldset>
          {problems.length > 0 && (
            <div className="space-y-2" role="alert">
              <p className="text-sm font-medium">{t('trigger.problems')}</p>
              <ProblemList problems={problems} />
            </div>
          )}
          <FormError error={error} />
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => { setOpen(false); }}>
              {t('common:actions.cancel')}
            </Button>
            <Button type="submit" disabled={busy}>
              {t('trigger.submit')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function ValidateDialog({ projectId }: { projectId: string }) {
  const { t } = useTranslation(['pipeline', 'common']);
  const fieldId = useId();
  const [content, setContent] = useState('');
  const [result, setResult] = useState<{ valid: boolean; problems: PipelineProblem[]; jobs: number; stages: number } | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);

  const check = async () => {
    setBusy(true);
    setError(null);
    try {
      const res = await validatePipeline(projectId, { content });
      const def = res.definition as { jobs?: unknown[]; stages?: unknown[] } | null;
      setResult({ valid: res.valid, problems: res.problems, jobs: def?.jobs?.length ?? 0, stages: def?.stages?.length ?? 0 });
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog>
      <DialogTrigger asChild>
        <Button variant="outline">
          <FileCheck2 aria-hidden />
          {t('runs.check')}
        </Button>
      </DialogTrigger>
      <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{t('validate.title')}</DialogTitle>
          <DialogDescription>{t('validate.description')}</DialogDescription>
        </DialogHeader>
        <div className="space-y-3">
          <Label htmlFor={fieldId}>{t('validate.label')}</Label>
          <textarea
            id={fieldId}
            rows={14}
            spellCheck={false}
            value={content}
            onChange={(e) => {
              setContent(e.target.value);
              setResult(null);
            }}
            className="border-input bg-background focus-visible:border-ring focus-visible:ring-ring/50 w-full rounded-md border px-3 py-2 font-mono text-sm outline-none focus-visible:ring-[3px]"
          />
          {result?.valid && (
            <p role="status" className="text-success text-sm font-medium">
              {t('validate.valid', { jobs: result.jobs, stages: result.stages })}
            </p>
          )}
          {result && !result.valid && (
            <div className="space-y-2" role="alert">
              <p className="text-sm font-medium">{t('validate.invalid', { count: result.problems.length })}</p>
              <ProblemList problems={result.problems} />
            </div>
          )}
          <FormError error={error} />
        </div>
        <DialogFooter>
          <Button disabled={busy || !content.trim()} onClick={() => void check()}>
            {t('validate.check')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function RunRow({ run, base }: { run: Run; base: string }) {
  const { t } = useTranslation('pipeline');
  const fmt = useFormat();
  const elapsed = useElapsed();
  const invalid = run.problems.length > 0;
  return (
    <TableRow data-testid={`run-${String(run.number)}`}>
      <TableCell className="whitespace-nowrap">
        <Link to={`${base}/${run.id}`} className="font-medium underline-offset-4 hover:underline">
          #{fmt.number(run.number)}
        </Link>
      </TableCell>
      <TableCell>
        <RunStatusBadge status={run.status} />
      </TableCell>
      <TableCell className="max-w-72 min-w-48 whitespace-normal">
        <Link to={`${base}/${run.id}`} className="line-clamp-1 break-all hover:underline">
          {invalid ? t('runs.invalidFile') : run.title || run.commit_sha.slice(0, 7)}
        </Link>
        <p className="text-muted-foreground flex flex-wrap items-center gap-x-3 font-mono text-xs">
          <span className="inline-flex items-center gap-1">
            <GitBranch aria-hidden className="size-3" />
            {run.ref_name}
          </span>
          <span className="inline-flex items-center gap-1">
            <GitCommitHorizontal aria-hidden className="size-3" />
            {run.commit_sha.slice(0, 7)}
          </span>
        </p>
      </TableCell>
      <TableCell className="hidden text-sm md:table-cell">
        {t(`triggers.${run.trigger}`)}
        {(run.created_by_name || run.actor_name) && (
          <span className="text-muted-foreground block text-xs">{t('runs.by', { name: run.created_by_name || run.actor_name })}</span>
        )}
      </TableCell>
      <TableCell className="hidden text-sm whitespace-nowrap lg:table-cell">{elapsed(run.started_at, run.finished_at)}</TableCell>
      <TableCell className="text-sm whitespace-nowrap">{fmt.relative(run.created_at)}</TableCell>
    </TableRow>
  );
}

export function RunsPage() {
  const { t } = useTranslation('pipeline');
  const org = useCurrentOrg();
  const project = useCurrentProject();
  const [status, setStatus] = useState<RunStatus | ''>('');
  const [trigger, setTrigger] = useState<RunTrigger | ''>('');
  const runs = useListRuns(project.id, { limit: 50, ...(status ? { status } : {}), ...(trigger ? { trigger } : {}) }, { query: { refetchInterval: 15_000 } });
  const repo = useGetRepository(project.id, { query: { retry: false } });
  const canTrigger = canProject(project, ProjectAction.pipelinetrigger);
  const base = `/o/${org.slug}/projects/${project.id}/runs`;
  const noRepo = repo.isError && hasCode(repo.error, 'REPOSITORY_NOT_FOUND');
  const filtered = Boolean(status || trigger);

  return (
    <section className="space-y-4" aria-labelledby="runs-title">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h2 id="runs-title" className="text-lg font-semibold">
            {t('runs.title')}
          </h2>
          <p className="text-muted-foreground text-sm">{t('runs.description')}</p>
        </div>
        <div className="flex flex-wrap gap-2">
          <ValidateDialog projectId={project.id} />
          {canTrigger && !noRepo && <TriggerDialog projectId={project.id} defaultBranch={project.default_branch} runBase={base} />}
        </div>
      </div>
      <div className="flex flex-wrap gap-2">
        <select
          aria-label={t('runs.columns.status')}
          className={cn(selectClass)}
          value={status}
          onChange={(e) => {
            setStatus(e.target.value as RunStatus | '');
          }}
        >
          <option value="">{t('runs.allStatuses')}</option>
          {STATUSES.map((s) => (
            <option key={s} value={s}>
              {t(`status.${s}`)}
            </option>
          ))}
        </select>
        <select
          aria-label={t('runs.columns.trigger')}
          className={cn(selectClass)}
          value={trigger}
          onChange={(e) => {
            setTrigger(e.target.value as RunTrigger | '');
          }}
        >
          <option value="">{t('runs.allTriggers')}</option>
          {TRIGGERS.map((s) => (
            <option key={s} value={s}>
              {t(`triggers.${s}`)}
            </option>
          ))}
        </select>
      </div>
      {runs.isPending ? (
        <LoadingState />
      ) : runs.isError ? (
        <ErrorState error={runs.error} onRetry={() => void runs.refetch()} />
      ) : runs.data.items.length === 0 ? (
        <EmptyState title={filtered ? t('runs.noMatch') : t('runs.empty')} description={filtered ? undefined : noRepo ? t('runs.emptyNoRepo') : t('runs.emptyDescription')} />
      ) : (
        <Card className="gap-0 overflow-x-auto p-0">
          <Table data-testid="runs-table">
            <TableHeader>
              <TableRow>
                <TableHead>{t('runs.columns.run')}</TableHead>
                <TableHead>{t('runs.columns.status')}</TableHead>
                <TableHead>{t('runs.columns.commit')}</TableHead>
                <TableHead className="hidden md:table-cell">{t('runs.columns.trigger')}</TableHead>
                <TableHead className="hidden lg:table-cell">{t('runs.columns.duration')}</TableHead>
                <TableHead>{t('runs.columns.created')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {runs.data.items.map((r) => (
                <RunRow key={r.id} run={r} base={base} />
              ))}
            </TableBody>
          </Table>
        </Card>
      )}
    </section>
  );
}
