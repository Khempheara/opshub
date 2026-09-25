import { zodResolver } from '@hookform/resolvers/zod';
import { useQueryClient } from '@tanstack/react-query';
import { Pencil, Plus, Server, Trash2 } from 'lucide-react';
import { useState } from 'react';
import { useForm } from 'react-hook-form';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';
import { useCurrentOrg } from '@/app/org';
import { OrgAction, usePermissions } from '@/app/permissions';
import { ConfirmDialog } from '@/components/common/ConfirmDialog';
import { CopyButton } from '@/components/common/CopyButton';
import { TextField } from '@/components/common/Field';
import { PageHeader } from '@/components/common/PageHeader';
import { EmptyState, ErrorState, FormError, LoadingState } from '@/components/common/States';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card } from '@/components/ui/card';
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from '@/components/ui/dialog';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table';
import { useFormat } from '@/i18n/useFormat';
import { applyFieldErrors, errorMessage, hasCode } from '@/lib/api/errors';
import type { RegistrationToken, Runner, RunnerStatus } from '@/lib/api/generated/model';
import {
  createRunnerRegistrationToken,
  deleteRunner,
  getListRunnersQueryKey,
  updateRunner,
  useListRunners,
} from '@/lib/api/generated/runners/runners';
import { ifMatch } from '@/lib/api/idempotency';
import { cn } from '@/lib/utils';
import { editSchema, parseLabels, registerSchema, type EditValues, type RegisterValues } from './runnerSchema';

const statusDot: Record<RunnerStatus, string> = {
  online: 'bg-success',
  offline: 'bg-muted-foreground/50',
  disabled: 'bg-amber-500',
};

function StatusLabel({ status }: { status: RunnerStatus }) {
  const { t } = useTranslation('runner');
  return (
    <span className="inline-flex items-center gap-2 text-sm" data-status={status}>
      <span aria-hidden className={cn('size-2 shrink-0 rounded-full', statusDot[status])} />
      {t(`status.${status}`)}
    </span>
  );
}

/** Messages from zod are i18n keys; server field errors arrive translated. */
function useFieldMessage() {
  const { t } = useTranslation(['runner', 'errors']);
  return (m: string | undefined, param?: number) =>
    m && (m.startsWith('errors:') || m.startsWith('runner:') ? t(m as 'errors:rules.max', { param }) : m);
}

function RegisterDialog({ orgId, onCreated }: { orgId: string; onCreated: (t: RegistrationToken) => void }) {
  const { t } = useTranslation(['runner', 'common']);
  const msg = useFieldMessage();
  const [open, setOpen] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const form = useForm<RegisterValues>({ resolver: zodResolver(registerSchema), defaultValues: { labels: '' } });

  const onSubmit = async (v: RegisterValues) => {
    setError(null);
    try {
      const token = await createRunnerRegistrationToken(orgId, { labels: parseLabels(v.labels) });
      setOpen(false);
      form.reset();
      onCreated(token);
    } catch (err) {
      if (!applyFieldErrors(err, form.setError, ['labels'])) setError(err);
    }
  };

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button>
          <Plus aria-hidden />
          {t('register.button')}
        </Button>
      </DialogTrigger>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('register.title')}</DialogTitle>
          <DialogDescription>{t('register.description')}</DialogDescription>
        </DialogHeader>
        <form noValidate className="space-y-4" onSubmit={(e) => void form.handleSubmit(onSubmit)(e)}>
          <TextField
            label={t('form.labels')}
            hint={t('form.labelsHint')}
            placeholder={t('form.labelsPlaceholder')}
            autoComplete="off"
            error={msg(form.formState.errors.labels?.message)}
            {...form.register('labels')}
          />
          <FormError error={error} />
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => { setOpen(false); }}>
              {t('common:actions.cancel')}
            </Button>
            <Button type="submit" disabled={form.formState.isSubmitting}>
              {t('register.create')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

/** Shows a new registration token once, with the commands that use it. */
function TokenDialog({ token, onClose }: { token: RegistrationToken | null; onClose: () => void }) {
  const { t } = useTranslation(['runner', 'common']);
  const fmt = useFormat();
  const origin = window.location.origin;
  const binary = token ? `opshub-runner register --url ${origin} --token ${token.token}\nopshub-runner run` : '';
  const docker = token
    ? `docker run -d --name opshub-runner --restart unless-stopped \\\n  --group-add "$(stat -c %g /var/run/docker.sock)" \\\n  -v /var/run/docker.sock:/var/run/docker.sock \\\n  -v opshub-runner:/var/lib/opshub-runner \\\n  -e OPSHUB_URL=${origin} \\\n  -e OPSHUB_REGISTRATION_TOKEN=${token.token} \\\n  opshub-runner`
    : '';
  return (
    <Dialog open={token !== null} onOpenChange={(o) => { if (!o) onClose(); }}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{t('token.title')}</DialogTitle>
          <DialogDescription>
            {token && t('token.body', { time: fmt.dateTime(token.expires_at, { timeStyle: 'short', dateStyle: 'medium' }) })}
          </DialogDescription>
        </DialogHeader>
        {token && (
          <div className="min-w-0 space-y-4">
            <div className="space-y-2">
              <code className="bg-muted block rounded-md p-3 font-mono text-sm break-all" data-testid="registration-token">
                {token.token}
              </code>
              <CopyButton value={token.token} />
            </div>
            <div className="space-y-2">
              <p className="text-sm font-medium">{t('token.binary')}</p>
              <pre className="bg-muted overflow-x-auto rounded-md p-3 font-mono text-xs">{binary}</pre>
              <CopyButton value={binary} label={t('token.copyCommand')} />
            </div>
            <div className="space-y-2">
              <p className="text-sm font-medium">{t('token.docker')}</p>
              <pre className="bg-muted overflow-x-auto rounded-md p-3 font-mono text-xs">{docker}</pre>
              <CopyButton value={docker} label={t('token.copyCommand')} />
              <p className="text-muted-foreground text-xs">{t('token.socketWarning')}</p>
            </div>
          </div>
        )}
        <DialogFooter>
          <Button onClick={onClose}>{t('common:actions.done')}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function EditDialog({ runner, onSaved }: { runner: Runner; onSaved: () => Promise<unknown> }) {
  const { t } = useTranslation(['runner', 'common']);
  const msg = useFieldMessage();
  const [open, setOpen] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const defaults = { name: runner.name, labels: runner.labels.join(', '), maxConcurrency: String(runner.max_concurrency) };
  const form = useForm<EditValues>({ resolver: zodResolver(editSchema), defaultValues: defaults });
  const errs = form.formState.errors;

  const onSubmit = async (v: EditValues) => {
    setError(null);
    try {
      await updateRunner(
        runner.id,
        { name: v.name, labels: parseLabels(v.labels), max_concurrency: Number(v.maxConcurrency), disabled: runner.status === 'disabled' },
        ifMatch(runner.row_version),
      );
      toast.success(t('edit.saved'));
      setOpen(false);
    } catch (err) {
      // A version conflict (someone else changed the runner) is shown as a form error.
      if (hasCode(err, 'VERSION_CONFLICT') || !applyFieldErrors(err, form.setError, ['name', 'labels'])) setError(err);
    }
    await onSaved();
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        setOpen(o);
        if (o) {
          form.reset(defaults);
          setError(null);
        }
      }}
    >
      <DialogTrigger asChild>
        <Button variant="ghost" size="icon" aria-label={t('edit.buttonFor', { name: runner.name })}>
          <Pencil aria-hidden />
        </Button>
      </DialogTrigger>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('edit.title', { name: runner.name })}</DialogTitle>
        </DialogHeader>
        <form noValidate className="space-y-4" onSubmit={(e) => void form.handleSubmit(onSubmit)(e)}>
          <TextField label={t('form.name')} autoComplete="off" error={msg(errs.name?.message, 100)} {...form.register('name')} />
          <TextField label={t('form.labels')} hint={t('form.labelsHint')} autoComplete="off" error={msg(errs.labels?.message)} {...form.register('labels')} />
          <TextField
            label={t('form.maxConcurrency')}
            hint={t('form.maxConcurrencyHint')}
            inputMode="numeric"
            error={msg(errs.maxConcurrency?.message)}
            {...form.register('maxConcurrency')}
          />
          <FormError error={error} />
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => { setOpen(false); }}>
              {t('common:actions.cancel')}
            </Button>
            <Button type="submit" disabled={form.formState.isSubmitting}>
              {t('common:actions.save')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function RunnerActions({ runner, onChanged }: { runner: Runner; onChanged: () => Promise<unknown> }) {
  const { t } = useTranslation(['runner', 'common']);
  const disabled = runner.status === 'disabled';

  const setDisabled = async (value: boolean) => {
    try {
      await updateRunner(
        runner.id,
        { name: runner.name, labels: runner.labels, max_concurrency: runner.max_concurrency, disabled: value },
        ifMatch(runner.row_version),
      );
      toast.success(value ? t('disable.done') : t('enable.done'));
    } catch (err) {
      toast.error(errorMessage(err));
    }
    await onChanged();
  };

  return (
    <div className="flex items-center justify-end gap-1">
      <EditDialog runner={runner} onSaved={onChanged} />
      {disabled ? (
        <Button variant="outline" size="sm" onClick={() => void setDisabled(false)}>
          {t('enable.button')}
        </Button>
      ) : (
        <ConfirmDialog
          trigger={
            <Button variant="outline" size="sm">
              {t('disable.button')}
            </Button>
          }
          title={t('disable.title', { name: runner.name })}
          description={t('disable.body')}
          confirmLabel={t('disable.button')}
          destructive={false}
          onConfirm={() => setDisabled(true)}
        />
      )}
      <ConfirmDialog
        trigger={
          <Button variant="ghost" size="icon" aria-label={t('delete.buttonFor', { name: runner.name })}>
            <Trash2 aria-hidden />
          </Button>
        }
        title={t('delete.title', { name: runner.name })}
        description={runner.running_jobs > 0 ? t('delete.bodyRunning', { count: runner.running_jobs }) : t('delete.body')}
        confirmLabel={t('common:actions.delete')}
        onConfirm={async () => {
          try {
            await deleteRunner(runner.id);
            toast.success(t('delete.done'));
          } catch (err) {
            toast.error(errorMessage(err));
          }
          await onChanged();
        }}
      />
    </div>
  );
}

export function RunnersPage() {
  const { t } = useTranslation(['runner', 'common']);
  const fmt = useFormat();
  const org = useCurrentOrg();
  const perms = usePermissions(org.id);
  const queryClient = useQueryClient();
  const canManage = perms.can(OrgAction.runnerManage);
  // Status comes from heartbeats: refresh while the page is open.
  const runners = useListRunners(org.id, { query: { refetchInterval: 10_000 } });
  const [token, setToken] = useState<RegistrationToken | null>(null);
  const invalidate = () => queryClient.invalidateQueries({ queryKey: getListRunnersQueryKey(org.id) });

  return (
    <div className="space-y-6">
      <PageHeader
        title={t('title')}
        description={t('description')}
        actions={
          canManage ? (
            <RegisterDialog
              orgId={org.id}
              onCreated={(tok) => {
                setToken(tok);
              }}
            />
          ) : undefined
        }
      />
      {runners.isPending ? (
        <LoadingState />
      ) : runners.isError ? (
        <ErrorState error={runners.error} onRetry={() => void runners.refetch()} />
      ) : runners.data.items.length === 0 ? (
        <EmptyState title={t('empty.title')} description={canManage ? t('empty.manage') : t('empty.view')} />
      ) : (
        <Card className="gap-0 overflow-x-auto p-0">
          <Table data-testid="runners-table">
            <TableHeader>
              <TableRow>
                <TableHead>{t('columns.name')}</TableHead>
                <TableHead>{t('columns.status')}</TableHead>
                <TableHead>{t('columns.labels')}</TableHead>
                <TableHead>{t('columns.jobs')}</TableHead>
                <TableHead>{t('columns.platform')}</TableHead>
                <TableHead>{t('columns.lastSeen')}</TableHead>
                {canManage && <TableHead className="w-0" />}
              </TableRow>
            </TableHeader>
            <TableBody>
              {runners.data.items.map((r) => (
                <TableRow key={r.id} data-testid="runner-row">
                  <TableCell>
                    <div className="flex items-center gap-2 font-medium">
                      <Server aria-hidden className="text-muted-foreground size-4 shrink-0" />
                      <span className="break-all">{r.name}</span>
                    </div>
                    <span className="text-muted-foreground font-mono text-xs">{r.token_prefix}…</span>
                  </TableCell>
                  <TableCell>
                    <StatusLabel status={r.status} />
                  </TableCell>
                  <TableCell>
                    <div className="flex max-w-xs flex-wrap gap-1">
                      {r.labels.length === 0 ? (
                        <span className="text-muted-foreground text-sm">{t('noLabels')}</span>
                      ) : (
                        r.labels.map((l) => (
                          <Badge key={l} variant="secondary" className="font-mono">
                            {l}
                          </Badge>
                        ))
                      )}
                    </div>
                  </TableCell>
                  <TableCell className="text-sm whitespace-nowrap">
                    {t('jobs', { running: fmt.number(r.running_jobs), max: fmt.number(r.max_concurrency) })}
                  </TableCell>
                  <TableCell className="text-muted-foreground font-mono text-xs whitespace-nowrap">
                    {r.version ? `${r.version} · ${r.os}/${r.arch}` : '—'}
                  </TableCell>
                  <TableCell className="text-sm whitespace-nowrap">{r.last_seen_at ? fmt.relative(r.last_seen_at) : t('common:time.never')}</TableCell>
                  {canManage && (
                    <TableCell>
                      <RunnerActions runner={r} onChanged={invalidate} />
                    </TableCell>
                  )}
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Card>
      )}
      <TokenDialog
        token={token}
        onClose={() => {
          setToken(null);
        }}
      />
    </div>
  );
}
