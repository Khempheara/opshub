import { useQueryClient } from '@tanstack/react-query';
import { Pencil, Plus, ShieldCheck, Trash2, X } from 'lucide-react';
import { useId, useState } from 'react';
import { Controller, useFieldArray, useForm, useWatch } from 'react-hook-form';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';
import { ConfirmDialog } from '@/components/common/ConfirmDialog';
import { Field, TextField } from '@/components/common/Field';
import { EmptyState, ErrorState, FormError, LoadingState } from '@/components/common/States';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { Checkbox } from '@/components/ui/checkbox';
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { selectClass } from '@/features/org/constants';
import { applyFieldErrors, errorMessage, hasCode } from '@/lib/api/errors';
import {
  createEnvironment,
  deleteEnvironment,
  getListEnvironmentsQueryKey,
  updateEnvironment,
  useListEnvironments,
} from '@/lib/api/generated/environments/environments';
import { EnvironmentKind, ProjectAction, type Environment } from '@/lib/api/generated/model';
import { idempotencyHeaders, ifMatch, newIdempotencyKey } from '@/lib/api/idempotency';
import { cn } from '@/lib/utils';
import { canProject, useCurrentProject } from './context';
import { emptyEnvironmentForm, PROTECT_ROLES, toFormValues, toRequest, type EnvironmentFormValues } from './envForm';

const KINDS = Object.values(EnvironmentKind);
const NAME_PATTERN = /^[a-z0-9](?:[a-z0-9_-]{0,38}[a-z0-9])?$/;
const VAR_PATTERN = /^[A-Za-z_][A-Za-z0-9_]{0,63}$/;

const kindTone: Record<Environment['kind'], string> = {
  development: 'bg-sky-500/10 text-sky-700 dark:text-sky-300',
  staging: 'bg-amber-500/10 text-amber-700 dark:text-amber-300',
  production: 'bg-rose-500/10 text-rose-700 dark:text-rose-300',
};

function KindBadge({ kind }: { kind: Environment['kind'] }) {
  const { t } = useTranslation('project');
  return <span className={cn('rounded-md px-2 py-0.5 text-xs font-medium', kindTone[kind])}>{t(`kinds.${kind}`)}</span>;
}

/** Create (environment undefined) or edit an environment. */
function EnvironmentDialog({
  projectId,
  environment,
  open,
  onOpenChange,
}: {
  projectId: string;
  environment?: Environment;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const { t } = useTranslation(['project', 'common', 'errors']);
  const queryClient = useQueryClient();
  const [error, setError] = useState<unknown>(null);
  const [key] = useState(newIdempotencyKey);
  const approvalsId = useId();
  const branchesId = useId();
  const form = useForm<EnvironmentFormValues>({ values: environment ? toFormValues(environment) : emptyEnvironmentForm() });
  const variables = useFieldArray({ control: form.control, name: 'variables' });
  const isProtected = useWatch({ control: form.control, name: 'protected' });
  const errs = form.formState.errors;

  const onSubmit = async (v: EnvironmentFormValues) => {
    setError(null);
    let invalid = false;
    if (!environment && !NAME_PATTERN.test(v.name.trim())) {
      form.setError('name', { type: 'client', message: t('errors:rules.slug') });
      invalid = true;
    }
    v.variables.forEach((row, i) => {
      if (row.key.trim() && !VAR_PATTERN.test(row.key.trim())) {
        form.setError(`variables.${i}.key`, { type: 'client', message: t('errors:rules.variable_name') });
        invalid = true;
      }
    });
    if (v.protected && v.roles.length === 0) {
      form.setError('roles', { type: 'client', message: t('errors:rules.required') });
      invalid = true;
    }
    if (invalid) return;
    try {
      const body = toRequest(v);
      if (environment) {
        await updateEnvironment(environment.id, body, ifMatch(environment.version));
        toast.success(t('environments.saved'));
      } else {
        await createEnvironment(projectId, body, idempotencyHeaders(key));
        toast.success(t('environments.created'));
      }
      onOpenChange(false);
    } catch (err) {
      if (hasCode(err, 'ENVIRONMENT_NAME_TAKEN')) form.setError('name', { type: 'server', message: errorMessage(err) });
      else if (!applyFieldErrors(err, form.setError, ['name', 'kind'])) setError(err);
    }
    await queryClient.invalidateQueries({ queryKey: getListEnvironmentsQueryKey(projectId) });
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{environment ? t('environments.editTitle', { name: environment.name }) : t('environments.createTitle')}</DialogTitle>
        </DialogHeader>
        <form noValidate className="space-y-5" onSubmit={(e) => void form.handleSubmit(onSubmit)(e)}>
          <div className="grid gap-4 sm:grid-cols-2">
            <TextField
              label={t('environments.name')}
              hint={environment ? undefined : t('environments.nameHint')}
              autoComplete="off"
              spellCheck={false}
              disabled={Boolean(environment)}
              error={errs.name?.message}
              {...form.register('name')}
            />
            <Field label={t('environments.kind')}>
              {({ id }) => (
                <select id={id} className={cn(selectClass, 'w-full')} {...form.register('kind')}>
                  {KINDS.map((k) => (
                    <option key={k} value={k}>
                      {t(`kinds.${k}`)}
                    </option>
                  ))}
                </select>
              )}
            </Field>
          </div>

          <fieldset className="space-y-2">
            <legend className="text-sm font-medium">{t('environments.variables')}</legend>
            <p className="text-muted-foreground text-xs">{t('environments.variablesHint')}</p>
            {variables.fields.length === 0 && <p className="text-muted-foreground text-sm">{t('environments.noVariables')}</p>}
            <ul className="space-y-2">
              {variables.fields.map((f, i) => (
                <li key={f.id} className="flex items-start gap-2">
                  <div className="min-w-0 flex-1">
                    <Input
                      aria-label={t('environments.variableName')}
                      placeholder={t('environments.variableName')}
                      className="font-mono"
                      autoComplete="off"
                      spellCheck={false}
                      aria-invalid={Boolean(errs.variables?.[i]?.key) || undefined}
                      {...form.register(`variables.${i}.key`)}
                    />
                    {errs.variables?.[i]?.key && (
                      <p role="alert" className="text-destructive mt-1 text-xs">
                        {errs.variables[i].key.message}
                      </p>
                    )}
                  </div>
                  <Input
                    aria-label={t('environments.variableValue')}
                    placeholder={t('environments.variableValue')}
                    className="min-w-0 flex-1 font-mono"
                    autoComplete="off"
                    spellCheck={false}
                    {...form.register(`variables.${i}.value`)}
                  />
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon"
                    aria-label={t('environments.removeVariable', { name: form.getValues(`variables.${i}.key`) || String(i + 1) })}
                    onClick={() => {
                      variables.remove(i);
                    }}
                  >
                    <X aria-hidden />
                  </Button>
                </li>
              ))}
            </ul>
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => {
                variables.append({ key: '', value: '' });
              }}
            >
              <Plus aria-hidden />
              {t('environments.addVariable')}
            </Button>
          </fieldset>

          <fieldset className="space-y-3 rounded-lg border p-4">
            <Controller
              control={form.control}
              name="protected"
              render={({ field }) => (
                <div className="flex items-start gap-3">
                  <Checkbox
                    id={`${approvalsId}-protect`}
                    checked={field.value}
                    onCheckedChange={(c) => {
                      field.onChange(c === true);
                    }}
                    className="mt-1"
                  />
                  <div>
                    <Label htmlFor={`${approvalsId}-protect`}>{t('environments.protect')}</Label>
                    <p className="text-muted-foreground text-xs">{t('environments.protectHint')}</p>
                  </div>
                </div>
              )}
            />
            {isProtected && (
              <div className="space-y-4 ps-7">
                <div className="space-y-1.5">
                  <Label htmlFor={approvalsId}>{t('environments.approvals')}</Label>
                  <Input id={approvalsId} type="number" min={0} max={10} className="sm:max-w-32" {...form.register('requiredApprovals', { valueAsNumber: true })} />
                  <p className="text-muted-foreground text-xs">{t('environments.approvalsHint')}</p>
                </div>
                <div className="space-y-1.5">
                  <Label htmlFor={branchesId}>{t('environments.branches')}</Label>
                  <textarea
                    id={branchesId}
                    rows={3}
                    spellCheck={false}
                    className="border-input bg-background focus-visible:border-ring focus-visible:ring-ring/50 w-full rounded-md border px-3 py-2 font-mono text-sm outline-none focus-visible:ring-[3px]"
                    {...form.register('branches')}
                  />
                  <p className="text-muted-foreground text-xs">{t('environments.branchesHint')}</p>
                </div>
                <Controller
                  control={form.control}
                  name="roles"
                  render={({ field }) => (
                    <fieldset>
                      <legend className="mb-2 text-sm font-medium">{t('environments.roles')}</legend>
                      <div className="flex flex-wrap gap-4">
                        {PROTECT_ROLES.map((r) => (
                          <label key={r} className="flex items-center gap-2 text-sm">
                            <Checkbox
                              checked={field.value.includes(r)}
                              onCheckedChange={(c) => {
                                field.onChange(c === true ? [...field.value, r] : field.value.filter((x) => x !== r));
                              }}
                            />
                            {t(`common:roles.${r}`)}
                          </label>
                        ))}
                      </div>
                      {errs.roles?.message && (
                        <p role="alert" className="text-destructive mt-1 text-sm">
                          {errs.roles.message}
                        </p>
                      )}
                    </fieldset>
                  )}
                />
              </div>
            )}
          </fieldset>

          <FormError error={error} />
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => { onOpenChange(false); }}>
              {t('common:actions.cancel')}
            </Button>
            <Button type="submit" disabled={form.formState.isSubmitting}>
              {form.formState.isSubmitting ? t('common:actions.saving') : environment ? t('common:actions.save') : t('environments.create')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function EnvironmentCard({ env, canManage, onEdit }: { env: Environment; canManage: boolean; onEdit: () => void }) {
  const { t } = useTranslation(['project', 'common']);
  const queryClient = useQueryClient();
  const vars = Object.keys(env.variables).length;
  const p = env.protection;
  const branches = p && p.allowed_branches.length > 0 ? p.allowed_branches.join(', ') : t('environments.anyBranch');
  return (
    <Card data-testid={`environment-${env.name}`}>
      <CardHeader className="flex flex-row flex-wrap items-center gap-2 space-y-0">
        <CardTitle className="font-mono text-base">{env.name}</CardTitle>
        <KindBadge kind={env.kind} />
        {p && (
          <Badge variant="secondary" className="gap-1">
            <ShieldCheck aria-hidden className="size-3" />
            {t('environments.protected')}
          </Badge>
        )}
        {canManage && (
          <div className="ms-auto flex gap-1">
            <Button variant="ghost" size="sm" onClick={onEdit}>
              <Pencil aria-hidden />
              {t('environments.edit')}
            </Button>
            <ConfirmDialog
              trigger={
                <Button variant="ghost" size="sm">
                  <Trash2 aria-hidden />
                  {t('environments.delete')}
                </Button>
              }
              title={t('environments.deleteTitle', { name: env.name })}
              description={t('environments.deleteBody', { name: env.name })}
              confirmLabel={t('environments.delete')}
              onConfirm={async () => {
                try {
                  await deleteEnvironment(env.id);
                  toast.success(t('environments.deleted'));
                } catch (err) {
                  toast.error(errorMessage(err));
                }
                await queryClient.invalidateQueries({ queryKey: getListEnvironmentsQueryKey(env.project_id) });
              }}
            />
          </div>
        )}
      </CardHeader>
      <CardContent className="text-muted-foreground space-y-1 text-sm">
        <p>{t('environments.variableCount', { count: vars })}</p>
        {p && (
          <p>
            {t('environments.protectionSummary', { count: p.required_approvals, branches })} ·{' '}
            {p.allowed_roles.map((r) => t(`common:roles.${r}`)).join(', ')}
          </p>
        )}
      </CardContent>
    </Card>
  );
}

export function EnvironmentsPage() {
  const { t } = useTranslation('project');
  const project = useCurrentProject();
  const envs = useListEnvironments(project.id);
  const canManage = canProject(project, ProjectAction.environmentmanage);
  const [editing, setEditing] = useState<Environment | undefined>();
  const [open, setOpen] = useState(false);
  const [dialogKey, setDialogKey] = useState(0);

  const openDialog = (env?: Environment) => {
    setEditing(env);
    setDialogKey((k) => k + 1); // fresh form state and Idempotency-Key
    setOpen(true);
  };

  return (
    <section className="space-y-4" aria-labelledby="environments-title">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h2 id="environments-title" className="text-lg font-semibold">
            {t('environments.title')}
          </h2>
          <p className="text-muted-foreground text-sm">{t('environments.description')}</p>
        </div>
        {canManage && (
          <Button
            onClick={() => {
              openDialog();
            }}
          >
            <Plus aria-hidden />
            {t('environments.create')}
          </Button>
        )}
      </div>
      {envs.isPending ? (
        <LoadingState />
      ) : envs.isError ? (
        <ErrorState error={envs.error} onRetry={() => void envs.refetch()} />
      ) : envs.data.items.length === 0 ? (
        <EmptyState title={t('environments.empty')} description={t('environments.emptyDescription')} />
      ) : (
        <div className="grid gap-4 lg:grid-cols-2">
          {envs.data.items.map((env) => (
            <EnvironmentCard
              key={env.id}
              env={env}
              canManage={canManage}
              onEdit={() => {
                openDialog(env);
              }}
            />
          ))}
        </div>
      )}
      {canManage && <EnvironmentDialog key={dialogKey} projectId={project.id} environment={editing} open={open} onOpenChange={setOpen} />}
    </section>
  );
}
