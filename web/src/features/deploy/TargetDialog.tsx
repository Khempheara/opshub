import { useQueryClient } from '@tanstack/react-query';
import { useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';
import { Field, PasswordField, TextField } from '@/components/common/Field';
import { FormError } from '@/components/common/States';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog';
import { fieldErrorMessage } from '@/lib/api/errors';
import type { DeployTarget, DeployTargetKind } from '@/lib/api/generated/model';
import { createDeployTarget, getListDeployTargetsQueryKey, updateDeployTarget } from '@/lib/api/generated/deployments/deployments';
import { ifMatch } from '@/lib/api/idempotency';
import { asConfig, configOf, emptyForm, fieldErrors, fromTarget, toConfig, toCredentials, type TargetForm } from './targetForm';

// Examples shown in empty fields (technical values, not translated).
const HOSTS_EXAMPLE = 'web-1.internal\n10.0.0.12:2222';
const KEY_EXAMPLE = '-----BEGIN OPENSSH PRIVATE KEY-----';
const REGISTRY_EXAMPLE = 'ghcr.io';

const inputClass =
  'border-input bg-background focus-visible:border-ring focus-visible:ring-ring/50 block w-full rounded-md border px-3 py-2 text-sm outline-none focus-visible:ring-[3px]';

function TextArea({ label, hint, error, value, onChange, rows = 3, mono = false, placeholder }: {
  label: string;
  hint?: ReactNode;
  error?: string;
  value: string;
  onChange: (v: string) => void;
  rows?: number;
  mono?: boolean;
  placeholder?: string;
}) {
  return (
    <Field label={label} hint={hint} error={error}>
      {({ id, describedBy, invalid }) => (
        <textarea
          id={id}
          rows={rows}
          value={value}
          placeholder={placeholder}
          spellCheck={false}
          dir="ltr"
          aria-describedby={describedBy}
          aria-invalid={invalid || undefined}
          onChange={(e) => {
            onChange(e.target.value);
          }}
          className={`${inputClass} ${mono ? 'font-mono text-xs' : ''}`}
        />
      )}
    </Field>
  );
}

function Select<T extends string>({ label, value, options, onChange, error }: {
  label: string;
  value: T;
  options: { value: T; label: string }[];
  onChange: (v: T) => void;
  error?: string;
}) {
  return (
    <Field label={label} error={error}>
      {({ id, describedBy, invalid }) => (
        <select
          id={id}
          value={value}
          aria-describedby={describedBy}
          aria-invalid={invalid || undefined}
          onChange={(e) => {
            onChange(e.target.value as T);
          }}
          className="border-input bg-background block min-h-9 w-full rounded-md border px-3 py-1.5 text-sm"
        >
          {options.map((o) => (
            <option key={o.value} value={o.value}>
              {o.label}
            </option>
          ))}
        </select>
      )}
    </Field>
  );
}

/** Create (target undefined) or edit a deploy target. */
export function TargetDialog({ orgId, target, open, onOpenChange }: {
  orgId: string;
  target?: DeployTarget;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const { t } = useTranslation(['deploy', 'common']);
  const queryClient = useQueryClient();
  const [form, setForm] = useState<TargetForm>(() => (target ? fromTarget(target) : emptyForm));
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const editing = target !== undefined;
  const { fields, unmatched } = fieldErrors(error);
  const err = (k: keyof TargetForm) => (fields[k] ? fieldErrorMessage(fields[k]) : undefined);
  const set = <K extends keyof TargetForm>(k: K) => (v: TargetForm[K]) => {
    setForm((f) => ({ ...f, [k]: v }));
  };
  const text = (k: keyof TargetForm) => ({
    value: form[k],
    error: err(k),
    onChange: (e: { target: { value: string } }) => {
      setForm((f) => ({ ...f, [k]: e.target.value }));
    },
  });

  const submit = async () => {
    setError(null);
    setBusy(true);
    try {
      const config = toConfig(form, target ? configOf(target) : undefined);
      const credentials = toCredentials(form);
      if (editing) {
        await updateDeployTarget(target.id, { description: form.description, config: asConfig(config), credentials }, ifMatch(target.version));
        toast.success(t('targets.saved'));
      } else {
        await createDeployTarget(orgId, {
          name: form.name.trim(), kind: form.kind, description: form.description, config: asConfig(config), credentials: credentials ?? {},
        });
        toast.success(t('targets.created'));
      }
      await queryClient.invalidateQueries({ queryKey: getListDeployTargetsQueryKey(orgId) });
      onOpenChange(false);
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };

  const keepHint = editing ? t('targets.form.keepSecret') : undefined;
  const kinds: { value: DeployTargetKind; label: string }[] = [
    { value: 'ssh', label: t('kinds.ssh') },
    { value: 'docker', label: t('kinds.docker') },
    { value: 'kubernetes', label: t('kinds.kubernetes') },
  ];

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{editing ? t('targets.editTitle', { name: target.name }) : t('targets.createTitle')}</DialogTitle>
          <DialogDescription>{t('targets.form.intro')}</DialogDescription>
        </DialogHeader>
        <form
          noValidate
          className="space-y-4"
          onSubmit={(e) => {
            e.preventDefault();
            void submit();
          }}
        >
          <div className="grid gap-4 sm:grid-cols-2">
            <TextField label={t('targets.form.name')} hint={t('targets.form.nameHint')} autoComplete="off" disabled={editing} {...text('name')} />
            {editing ? (
              <Field label={t('targets.form.kind')}>{() => <p className="py-1.5 text-sm">{t(`kinds.${form.kind}`)}</p>}</Field>
            ) : (
              <Select label={t('targets.form.kind')} value={form.kind} options={kinds} onChange={set('kind')} />
            )}
          </div>
          <TextField label={t('targets.form.description')} autoComplete="off" {...text('description')} />

          {form.kind === 'ssh' && (
            <fieldset className="space-y-4">
              <legend className="mb-2 text-sm font-semibold">{t('targets.form.where')}</legend>
              <TextArea label={t('targets.form.hosts')} hint={t('targets.form.hostsHint')} mono value={form.hosts} onChange={set('hosts')} error={err('hosts')} placeholder={HOSTS_EXAMPLE} />
              <div className="grid gap-4 sm:grid-cols-2">
                <TextField label={t('targets.form.user')} autoComplete="off" {...text('user')} />
                <TextField label={t('targets.form.batchSize')} hint={t('targets.form.batchSizeHint')} inputMode="numeric" {...text('batchSize')} />
              </div>
              <TextArea label={t('targets.form.command')} hint={t('targets.form.commandHint')} mono rows={4} value={form.command} onChange={set('command')} error={err('command')} />
            </fieldset>
          )}

          {form.kind === 'docker' && (
            <fieldset className="space-y-4">
              <legend className="mb-2 text-sm font-semibold">{t('targets.form.where')}</legend>
              <Select
                label={t('targets.form.connection')}
                value={form.connection}
                error={err('connection')}
                onChange={set('connection')}
                options={[
                  { value: 'ssh', label: t('targets.form.connections.ssh') },
                  { value: 'tls', label: t('targets.form.connections.tls') },
                  { value: 'local', label: t('targets.form.connections.local') },
                ]}
              />
              {form.connection !== 'local' && (
                <div className="grid gap-4 sm:grid-cols-2">
                  <TextField label={t('targets.form.host')} hint={form.connection === 'tls' ? t('targets.form.hostTlsHint') : undefined} autoComplete="off" {...text('host')} />
                  {form.connection === 'ssh' && <TextField label={t('targets.form.user')} autoComplete="off" {...text('user')} />}
                </div>
              )}
              <div className="grid gap-4 sm:grid-cols-2">
                <TextField label={t('targets.form.container')} autoComplete="off" {...text('container')} />
                <TextField label={t('targets.form.replicas')} inputMode="numeric" {...text('replicas')} />
              </div>
              <div className="grid gap-4 sm:grid-cols-2">
                <TextArea label={t('targets.form.ports')} hint={t('targets.form.portsHint')} mono value={form.ports} onChange={set('ports')} error={err('ports')} />
                <TextArea label={t('targets.form.env')} hint={t('targets.form.envHint')} mono value={form.env} onChange={set('env')} error={err('env')} />
              </div>
              <div className="grid gap-4 sm:grid-cols-2">
                <TextField label={t('targets.form.network')} autoComplete="off" {...text('network')} />
                <Select
                  label={t('targets.form.restart')}
                  value={form.restart}
                  onChange={set('restart')}
                  options={['unless-stopped', 'always', 'on-failure', 'no'].map((v) => ({ value: v, label: v }))}
                />
              </div>
            </fieldset>
          )}

          {form.kind === 'kubernetes' && (
            <fieldset className="space-y-4">
              <legend className="mb-2 text-sm font-semibold">{t('targets.form.where')}</legend>
              <div className="grid gap-4 sm:grid-cols-2">
                <TextField label={t('targets.form.namespace')} autoComplete="off" {...text('namespace')} />
                <TextField label={t('targets.form.deployment')} autoComplete="off" {...text('deployment')} />
                <TextField label={t('targets.form.k8sContainer')} hint={t('targets.form.k8sContainerHint')} autoComplete="off" {...text('k8sContainer')} />
                <TextField label={t('targets.form.service')} hint={t('targets.form.serviceHint')} autoComplete="off" {...text('service')} />
              </div>
              <TextField label={t('targets.form.rolloutTimeout')} inputMode="numeric" {...text('rolloutTimeout')} />
            </fieldset>
          )}

          <fieldset className="space-y-4">
            <legend className="mb-2 text-sm font-semibold">{t('targets.form.health')}</legend>
            <div className="grid gap-4 sm:grid-cols-[1fr_10rem]">
              <TextField label={t('targets.form.healthUrl')} hint={t('targets.form.healthUrlHint')} autoComplete="off" dir="ltr" {...text('healthUrl')} />
              <TextField label={t('targets.form.healthTimeout')} inputMode="numeric" {...text('healthTimeout')} />
            </div>
          </fieldset>

          <fieldset className="space-y-4">
            <legend className="mb-1 text-sm font-semibold">{t('targets.form.credentials')}</legend>
            <p className="text-muted-foreground text-xs">{t('targets.form.credentialsHint')}</p>
            {editing && target.credentials.length > 0 && (
              <p className="flex flex-wrap items-center gap-1 text-xs">
                {t('targets.form.stored')}
                {target.credentials.map((c) => (
                  <Badge key={c} variant="secondary" className="font-mono">
                    {c}
                  </Badge>
                ))}
              </p>
            )}
            {form.kind === 'ssh' && (
              <Select
                label={t('targets.form.authMethod')}
                value={form.authMethod}
                onChange={set('authMethod')}
                options={[
                  { value: 'key', label: t('targets.form.privateKey') },
                  { value: 'password', label: t('targets.form.password') },
                ]}
              />
            )}
            {((form.kind === 'ssh' && form.authMethod === 'key') || (form.kind === 'docker' && form.connection === 'ssh')) && (
              <>
                <TextArea label={t('targets.form.privateKey')} hint={keepHint} mono rows={4} value={form.privateKey} onChange={set('privateKey')} error={err('privateKey')} placeholder={KEY_EXAMPLE} />
                <PasswordField label={t('targets.form.passphrase')} autoComplete="off" {...text('passphrase')} />
              </>
            )}
            {form.kind === 'ssh' && form.authMethod === 'password' && <PasswordField label={t('targets.form.password')} hint={keepHint} autoComplete="new-password" {...text('password')} />}
            {form.kind === 'docker' && form.connection === 'tls' && (
              <>
                <TextArea label={t('targets.form.caCert')} hint={keepHint} mono value={form.caCert} onChange={set('caCert')} />
                <TextArea label={t('targets.form.clientCert')} mono value={form.clientCert} onChange={set('clientCert')} error={err('clientCert')} />
                <TextArea label={t('targets.form.clientKey')} mono value={form.clientKey} onChange={set('clientKey')} />
              </>
            )}
            {form.kind === 'docker' && (
              <div className="grid gap-4 sm:grid-cols-3">
                <TextField label={t('targets.form.registryServer')} placeholder={REGISTRY_EXAMPLE} autoComplete="off" {...text('registryServer')} />
                <TextField label={t('targets.form.registryUsername')} autoComplete="off" {...text('registryUsername')} />
                <PasswordField label={t('targets.form.registryPassword')} autoComplete="new-password" {...text('registryPassword')} />
              </div>
            )}
            {form.kind === 'kubernetes' && (
              <TextArea label={t('targets.form.kubeconfig')} hint={editing ? keepHint : t('targets.form.kubeconfigHint')} mono rows={6} value={form.kubeconfig} onChange={set('kubeconfig')} error={err('kubeconfig')} />
            )}
          </fieldset>

          {unmatched && <FormError error={error} />}
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
            <Button type="submit" disabled={busy}>
              {editing ? t('common:actions.save') : t('common:actions.create')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
