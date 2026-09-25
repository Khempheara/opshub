import { useQueryClient } from '@tanstack/react-query';
import { History, KeyRound, Pencil, Plus, RotateCw, ShieldCheck, Trash2 } from 'lucide-react';
import { useId, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';
import { ConfirmDialog } from '@/components/common/ConfirmDialog';
import { CopyButton } from '@/components/common/CopyButton';
import { Field, PasswordField, TextField } from '@/components/common/Field';
import { EmptyState, ErrorState, FormError, LoadingState } from '@/components/common/States';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { Checkbox } from '@/components/ui/checkbox';
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog';
import { Label } from '@/components/ui/label';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table';
import { selectClass } from '@/features/org/constants';
import { canProject, useCurrentProject } from '@/features/project/context';
import { useFormat } from '@/i18n/useFormat';
import { errorMessage, fieldErrorMessage, hasCode } from '@/lib/api/errors';
import { ApiError } from '@/lib/api/fetcher';
import { useListEnvironments } from '@/lib/api/generated/environments/environments';
import { ProjectAction, type Secret } from '@/lib/api/generated/model';
import {
  createSecret,
  deleteSecret,
  getListSecretsQueryKey,
  rotateSecret,
  updateSecret,
  useListSecrets,
  useListSecretVersions,
} from '@/lib/api/generated/secrets/secrets';
import { ifMatch } from '@/lib/api/idempotency';
import { ALL_ENVIRONMENTS, creatableScopes, nameProblem, normalizeName, usageSnippet, valueTooLarge } from './secretForm';

/** The environment filter's value for project-wide secrets (the API's `environment_id=none`). */
const PROJECT_WIDE = 'none';
const textareaClass =
  'border-input bg-background focus-visible:border-ring focus-visible:ring-ring/50 block w-full rounded-md border px-3 py-2 font-mono text-xs outline-none focus-visible:ring-[3px]';

function fieldError(error: unknown, name: string): string | undefined {
  if (!(error instanceof ApiError)) return undefined;
  const fe = error.fieldErrors.find((f) => f.field === name);
  return fe ? fieldErrorMessage(fe) : undefined;
}

/** A write-only value: one line (hidden) or several (keys, certificates). */
function ValueField({ value, onChange, error, label }: { value: string; onChange: (v: string) => void; error?: string; label: string }) {
  const { t } = useTranslation('secret');
  const [multiline, setMultiline] = useState(value.includes('\n'));
  const toggleId = useId();
  return (
    <div className="space-y-2">
      {multiline ? (
        <Field label={label} hint={t('form.valueHint')} error={error}>
          {({ id, describedBy, invalid }) => (
            <textarea
              id={id}
              rows={6}
              dir="ltr"
              spellCheck={false}
              autoComplete="off"
              aria-describedby={describedBy}
              aria-invalid={invalid || undefined}
              className={textareaClass}
              value={value}
              onChange={(e) => {
                onChange(e.target.value);
              }}
            />
          )}
        </Field>
      ) : (
        <PasswordField
          label={label}
          hint={t('form.valueHint')}
          autoComplete="off"
          dir="ltr"
          value={value}
          error={error}
          onChange={(e) => {
            onChange(e.target.value);
          }}
        />
      )}
      <div className="flex items-center gap-2">
        <Checkbox
          id={toggleId}
          checked={multiline}
          onCheckedChange={(c) => {
            setMultiline(c === true);
          }}
        />
        <Label htmlFor={toggleId} className="text-sm font-normal">
          {t('form.multiline')}
        </Label>
      </div>
    </div>
  );
}

function CreateDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  const { t } = useTranslation(['secret', 'common', 'errors']);
  const p = useCurrentProject();
  const queryClient = useQueryClient();
  const envs = useListEnvironments(p.id);
  const scopes = creatableScopes(envs.data?.items ?? [], p.role);
  const [name, setName] = useState('');
  const [scope, setScope] = useState<string | null>(null);
  const [description, setDescription] = useState('');
  const [value, setValue] = useState('');
  const [error, setError] = useState<unknown>(null);
  const [clientErrors, setClientErrors] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);
  // Default scope: every environment when allowed, else the first environment the caller may use.
  const effectiveScope = scope ?? (scopes.all ? ALL_ENVIRONMENTS : (scopes.environments[0]?.id ?? ALL_ENVIRONMENTS));

  const submit = async () => {
    const errs: Record<string, string> = {};
    const problem = nameProblem(name);
    if (problem === 'reserved') errs.name = t('errors:rules.reserved', { param: 'OPSHUB_' });
    else if (problem) errs.name = t('errors:rules.secret_name');
    if (!value) errs.value = t('errors:rules.required');
    else if (valueTooLarge(value)) errs.value = t('form.tooLarge');
    setClientErrors(errs);
    setError(null);
    if (Object.keys(errs).length > 0) return;
    setBusy(true);
    try {
      await createSecret(p.id, {
        name,
        environment_id: effectiveScope === ALL_ENVIRONMENTS ? null : effectiveScope,
        description: description.trim(),
        value,
      });
      await queryClient.invalidateQueries({ queryKey: getListSecretsQueryKey(p.id) });
      toast.success(t('created', { name }));
      onOpenChange(false);
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };

  const nameErr = clientErrors.name ?? fieldError(error, 'name');
  const valueErr = clientErrors.value ?? fieldError(error, 'value');
  const shown = ['name', 'value', 'description', 'environment_id'];
  const other = error !== null && !(error instanceof ApiError && error.fieldErrors.length > 0 && error.fieldErrors.every((f) => shown.includes(f.field)));

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{t('createTitle')}</DialogTitle>
          <DialogDescription>{t('createIntro')}</DialogDescription>
        </DialogHeader>
        <form
          noValidate
          className="space-y-4"
          onSubmit={(e) => {
            e.preventDefault();
            void submit();
          }}
        >
          <TextField
            label={t('form.name')}
            hint={t('form.nameHint')}
            autoComplete="off"
            spellCheck={false}
            dir="ltr"
            className="[&_input]:font-mono"
            value={name}
            error={nameErr}
            onChange={(e) => {
              setName(normalizeName(e.target.value));
            }}
          />
          <Field label={t('form.scope')} hint={scopes.all ? t('form.scopeHint') : t('form.scopeHintDeveloper')} error={fieldError(error, 'environment_id')}>
            {({ id, describedBy }) => (
              <select
                id={id}
                aria-describedby={describedBy}
                className={selectClass}
                value={effectiveScope}
                onChange={(e) => {
                  setScope(e.target.value);
                }}
              >
                {scopes.all && <option value={ALL_ENVIRONMENTS}>{t('allEnvironments')}</option>}
                {scopes.environments.map((env) => (
                  <option key={env.id} value={env.id}>
                    {env.protection ? t('protectedEnvironment', { name: env.name }) : env.name}
                  </option>
                ))}
              </select>
            )}
          </Field>
          <TextField
            label={t('form.description')}
            autoComplete="off"
            value={description}
            error={fieldError(error, 'description')}
            onChange={(e) => {
              setDescription(e.target.value);
            }}
          />
          <ValueField label={t('form.value')} value={value} onChange={setValue} error={valueErr} />
          {other && <FormError error={error} />}
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => {
                onOpenChange(false);
              }}
            >
              {t('common:actions.cancel')}
            </Button>
            <Button type="submit" disabled={busy || (!scopes.all && scopes.environments.length === 0)}>
              {t('common:actions.create')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function RotateDialog({ secret, onClose }: { secret: Secret; onClose: () => void }) {
  const { t } = useTranslation(['secret', 'common', 'errors']);
  const queryClient = useQueryClient();
  const [value, setValue] = useState('');
  const [error, setError] = useState<unknown>(null);
  const [clientError, setClientError] = useState<string>();
  const [busy, setBusy] = useState(false);

  const valueError = fieldError(error, 'value');

  const submit = async () => {
    setError(null);
    const problem = !value ? t('errors:rules.required') : valueTooLarge(value) ? t('form.tooLarge') : undefined;
    setClientError(problem);
    if (problem) return;
    setBusy(true);
    try {
      const s = await rotateSecret(secret.id, { value });
      await queryClient.invalidateQueries({ queryKey: getListSecretsQueryKey(secret.project_id) });
      toast.success(t('rotated', { name: secret.name, version: s.current_version }));
      onClose();
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog
      open
      onOpenChange={(o) => {
        if (!o) onClose();
      }}
    >
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{t('rotateTitle', { name: secret.name })}</DialogTitle>
          <DialogDescription>{t('rotateIntro')}</DialogDescription>
        </DialogHeader>
        <form
          noValidate
          className="space-y-4"
          onSubmit={(e) => {
            e.preventDefault();
            void submit();
          }}
        >
          <ValueField label={t('form.newValue')} value={value} onChange={setValue} error={clientError ?? valueError} />
          {error !== null && !valueError && <FormError error={error} />}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              {t('common:actions.cancel')}
            </Button>
            <Button type="submit" disabled={busy}>
              {t('rotate')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function DescriptionDialog({ secret, onClose }: { secret: Secret; onClose: () => void }) {
  const { t } = useTranslation(['secret', 'common']);
  const queryClient = useQueryClient();
  const [description, setDescription] = useState(secret.description);
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const descriptionError = fieldError(error, 'description');

  const submit = async () => {
    setBusy(true);
    setError(null);
    try {
      await updateSecret(secret.id, { description: description.trim() }, ifMatch(secret.version));
      await queryClient.invalidateQueries({ queryKey: getListSecretsQueryKey(secret.project_id) });
      toast.success(t('saved'));
      onClose();
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog
      open
      onOpenChange={(o) => {
        if (!o) onClose();
      }}
    >
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{t('editTitle', { name: secret.name })}</DialogTitle>
        </DialogHeader>
        <form
          noValidate
          className="space-y-4"
          onSubmit={(e) => {
            e.preventDefault();
            void submit();
          }}
        >
          <TextField
            label={t('form.description')}
            autoComplete="off"
            value={description}
            error={descriptionError}
            onChange={(e) => {
              setDescription(e.target.value);
            }}
          />
          {error !== null && !descriptionError && <FormError error={error} />}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              {t('common:actions.cancel')}
            </Button>
            <Button type="submit" disabled={busy}>
              {t('common:actions.save')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function HistoryDialog({ secret, onClose }: { secret: Secret; onClose: () => void }) {
  const { t } = useTranslation(['secret', 'common']);
  const fmt = useFormat();
  const versions = useListSecretVersions(secret.id);
  const snippet = usageSnippet(secret.name, secret.environment_name);
  return (
    <Dialog
      open
      onOpenChange={(o) => {
        if (!o) onClose();
      }}
    >
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{t('historyTitle', { name: secret.name })}</DialogTitle>
          <DialogDescription>{t('historyIntro')}</DialogDescription>
        </DialogHeader>
        {versions.isPending ? (
          <LoadingState rows={2} />
        ) : versions.isError ? (
          <ErrorState error={versions.error} onRetry={() => void versions.refetch()} />
        ) : (
          <ol className="divide-y rounded-md border" data-testid="secret-versions">
            {versions.data.items.map((v) => (
              <li key={v.version} className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1 px-3 py-2 text-sm">
                <span className="font-medium">
                  {t('versionLabel', { version: v.version })}
                  {v.current && <span className="text-success ms-2 text-xs">{t('current')}</span>}
                </span>
                <span className="text-muted-foreground text-xs">
                  {t('versionBy', { name: v.created_by_name ?? t('unknownUser'), time: fmt.dateTime(v.created_at, { dateStyle: 'medium', timeStyle: 'short' }) })}
                  {v.destroyed_at && <span className="block">{t('destroyed', { time: fmt.relative(v.destroyed_at) })}</span>}
                </span>
              </li>
            ))}
          </ol>
        )}
        <div className="space-y-2">
          <p className="text-sm font-medium">{t('usage')}</p>
          <pre className="bg-muted overflow-x-auto rounded-md p-3 font-mono text-xs" dir="ltr">
            {snippet}
          </pre>
          <CopyButton value={snippet} />
        </div>
        <DialogFooter>
          <Button onClick={onClose}>{t('common:actions.close')}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

type Open = { kind: 'rotate' | 'edit' | 'history'; secret: Secret } | null;

/** Project → Secrets: write-only values, listed by scope. */
export function SecretsPage() {
  const { t } = useTranslation(['secret', 'common']);
  const p = useCurrentProject();
  const fmt = useFormat();
  const queryClient = useQueryClient();
  const [filter, setFilter] = useState('');
  const [creating, setCreating] = useState(false);
  const [open, setOpen] = useState<Open>(null);
  const envs = useListEnvironments(p.id);
  const secrets = useListSecrets(p.id, filter ? { environment_id: filter } : undefined);
  const canCreate = canProject(p, ProjectAction.secretcreate);

  if (!canProject(p, ProjectAction.secretlist)) {
    return <EmptyState title={t('noAccess')} description={t('noAccessHint')} />;
  }

  const remove = async (s: Secret) => {
    try {
      await deleteSecret(s.id);
      await queryClient.invalidateQueries({ queryKey: getListSecretsQueryKey(p.id) });
      toast.success(t('deleted', { name: s.name }));
    } catch (err) {
      if (hasCode(err, 'SECRET_NOT_FOUND')) await queryClient.invalidateQueries({ queryKey: getListSecretsQueryKey(p.id) });
      toast.error(errorMessage(err));
    }
  };

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div className="min-w-0 space-y-1">
          <h2 className="flex items-center gap-2 text-lg font-semibold">
            <KeyRound aria-hidden className="text-muted-foreground size-5" />
            {t('title')}
          </h2>
          <p className="text-muted-foreground max-w-2xl text-sm">{t('intro')}</p>
        </div>
        {canCreate && (
          <Button
            onClick={() => {
              setCreating(true);
            }}
          >
            <Plus aria-hidden />
            {t('add')}
          </Button>
        )}
      </div>

      <div className="flex flex-wrap items-center gap-2">
        <label className="text-sm" htmlFor="secret-filter">
          {t('filter')}
        </label>
        <select
          id="secret-filter"
          className={`${selectClass} w-auto`}
          value={filter}
          onChange={(e) => {
            setFilter(e.target.value);
          }}
        >
          <option value="">{t('filterAll')}</option>
          <option value={PROJECT_WIDE}>{t('allEnvironments')}</option>
          {(envs.data?.items ?? []).map((env) => (
            <option key={env.id} value={env.id}>
              {env.name}
            </option>
          ))}
        </select>
      </div>

      {secrets.isPending ? (
        <LoadingState />
      ) : secrets.isError ? (
        <ErrorState error={secrets.error} onRetry={() => void secrets.refetch()} />
      ) : secrets.data.items.length === 0 ? (
        <EmptyState title={filter ? t('emptyFiltered') : t('empty')} description={canCreate && !filter ? t('emptyHint') : undefined} />
      ) : (
        <Card className="gap-0 overflow-x-auto p-0">
          <Table data-testid="secrets-table">
            <TableHeader>
              <TableRow>
                <TableHead>{t('columns.name')}</TableHead>
                <TableHead>{t('columns.scope')}</TableHead>
                <TableHead>{t('columns.updated')}</TableHead>
                <TableHead>
                  <span className="sr-only">{t('columns.actions')}</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {secrets.data.items.map((s) => (
                <TableRow key={s.id} data-testid="secret-row">
                  <TableCell className="max-w-72">
                    <span className="font-mono text-sm font-medium break-all" dir="ltr">
                      {s.name}
                    </span>
                    {s.description && <span className="text-muted-foreground block text-xs break-words">{s.description}</span>}
                  </TableCell>
                  <TableCell>
                    <span className="inline-flex items-center gap-1.5 text-sm whitespace-nowrap">
                      {s.protected && <ShieldCheck aria-hidden className="text-muted-foreground size-4" />}
                      {s.environment_name ?? t('allEnvironments')}
                    </span>
                  </TableCell>
                  <TableCell className="text-muted-foreground text-xs whitespace-nowrap">
                    {t('versionLabel', { version: s.current_version })} · {fmt.relative(s.rotated_at)}
                  </TableCell>
                  <TableCell>
                    <div className="flex justify-end gap-1">
                      <Button
                        variant="ghost"
                        size="icon"
                        aria-label={t('historyFor', { name: s.name })}
                        title={t('history')}
                        onClick={() => {
                          setOpen({ kind: 'history', secret: s });
                        }}
                      >
                        <History aria-hidden />
                      </Button>
                      {s.can_manage && (
                        <>
                          <Button
                            variant="ghost"
                            size="icon"
                            aria-label={t('rotateFor', { name: s.name })}
                            title={t('rotate')}
                            onClick={() => {
                              setOpen({ kind: 'rotate', secret: s });
                            }}
                          >
                            <RotateCw aria-hidden />
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon"
                            aria-label={t('editFor', { name: s.name })}
                            title={t('edit')}
                            onClick={() => {
                              setOpen({ kind: 'edit', secret: s });
                            }}
                          >
                            <Pencil aria-hidden />
                          </Button>
                          <ConfirmDialog
                            trigger={
                              <Button variant="ghost" size="icon" aria-label={t('deleteFor', { name: s.name })} title={t('common:actions.delete')}>
                                <Trash2 aria-hidden />
                              </Button>
                            }
                            title={t('deleteTitle', { name: s.name })}
                            description={t('deleteBody')}
                            confirmLabel={t('common:actions.delete')}
                            onConfirm={() => remove(s)}
                          />
                        </>
                      )}
                    </div>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Card>
      )}

      <Card>
        <CardHeader>
          <CardTitle className="text-base">{t('howTitle')}</CardTitle>
        </CardHeader>
        <CardContent className="text-muted-foreground space-y-2 text-sm">
          <p>{t('howJobs')}</p>
          <p>{t('howMasked')}</p>
          <p>{t('howPullRequests')}</p>
        </CardContent>
      </Card>

      {creating && (
        <CreateDialog
          open
          onOpenChange={(o) => {
            if (!o) setCreating(false);
          }}
        />
      )}
      {open?.kind === 'rotate' && (
        <RotateDialog
          secret={open.secret}
          onClose={() => {
            setOpen(null);
          }}
        />
      )}
      {open?.kind === 'edit' && (
        <DescriptionDialog
          secret={open.secret}
          onClose={() => {
            setOpen(null);
          }}
        />
      )}
      {open?.kind === 'history' && (
        <HistoryDialog
          secret={open.secret}
          onClose={() => {
            setOpen(null);
          }}
        />
      )}
    </div>
  );
}
