import { useQueryClient } from '@tanstack/react-query';
import { useId, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router';
import { toast } from 'sonner';
import { Field, TextField } from '@/components/common/Field';
import { FormError } from '@/components/common/States';
import { Button } from '@/components/ui/button';
import { Checkbox } from '@/components/ui/checkbox';
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog';
import { Label } from '@/components/ui/label';
import { selectClass } from '@/features/org/constants';
import { MonitorKind, type Monitor } from '@/lib/api/generated/model';
import { createMonitor, getGetMonitorQueryKey, getListMonitorsQueryKey, updateMonitor } from '@/lib/api/generated/monitoring/monitoring';
import { ifMatch } from '@/lib/api/idempotency';
import { emptyMonitorForm, fieldError, hasOtherErrors, monitorForm, monitorRequest, type MonitorForm } from './forms';

const FIELDS = ['name', 'kind', 'target', 'interval_seconds', 'timeout_ms', 'settings', 'labels'];

/** Create (monitor undefined) or edit a monitor. */
export function MonitorDialog({ orgId, orgSlug, monitor, onClose }: { orgId: string; orgSlug: string; monitor?: Monitor; onClose: () => void }) {
  const { t } = useTranslation(['monitoring', 'common']);
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const enabledId = useId();
  const [form, setForm] = useState<MonitorForm>(() => (monitor ? monitorForm(monitor) : emptyMonitorForm));
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const editing = monitor !== undefined;
  const set = <K extends keyof MonitorForm>(k: K, v: MonitorForm[K]) => {
    setForm((f) => ({ ...f, [k]: v }));
  };
  const err = (name: string) => fieldError(error, name);

  const submit = async () => {
    setBusy(true);
    setError(null);
    try {
      if (editing) {
        await updateMonitor(monitor.id, monitorRequest(form, false), ifMatch(monitor.version));
        await queryClient.invalidateQueries({ queryKey: getGetMonitorQueryKey(monitor.id) });
        toast.success(t('monitors.saved'));
      } else {
        const m = await createMonitor(orgId, monitorRequest(form, true));
        toast.success(t('monitors.created'));
        await navigate(`/o/${orgSlug}/monitoring/monitors/${m.id}`);
      }
      await queryClient.invalidateQueries({ queryKey: getListMonitorsQueryKey(orgId) });
      onClose();
    } catch (e) {
      setError(e);
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
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>{editing ? t('monitors.editTitle', { name: monitor.name }) : t('monitors.createTitle')}</DialogTitle>
          <DialogDescription>{t('monitors.intro')}</DialogDescription>
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
            <TextField
              label={t('monitors.form.name')}
              autoComplete="off"
              value={form.name}
              error={err('name')}
              onChange={(e) => {
                set('name', e.target.value);
              }}
            />
            <Field label={t('monitors.form.kind')} error={err('kind')}>
              {({ id }) =>
                editing ? (
                  <p id={id} className="py-1.5 text-sm">
                    {t(`kinds.${form.kind}`)}
                  </p>
                ) : (
                  <select
                    id={id}
                    className={selectClass}
                    value={form.kind}
                    onChange={(e) => {
                      set('kind', e.target.value as MonitorForm['kind']);
                    }}
                  >
                    {Object.values(MonitorKind).map((k) => (
                      <option key={k} value={k}>
                        {t(`kinds.${k}`)}
                      </option>
                    ))}
                  </select>
                )
              }
            </Field>
          </div>
          <TextField
            label={t('monitors.form.target')}
            hint={t(`monitors.form.targetHint.${form.kind}`)}
            autoComplete="off"
            dir="ltr"
            value={form.target}
            error={err('target')}
            onChange={(e) => {
              set('target', e.target.value);
            }}
          />
          <div className="grid gap-4 sm:grid-cols-2">
            <TextField
              label={t('monitors.form.interval')}
              inputMode="numeric"
              value={form.interval}
              error={err('interval_seconds')}
              onChange={(e) => {
                set('interval', e.target.value);
              }}
            />
            <TextField
              label={t('monitors.form.timeout')}
              inputMode="decimal"
              value={form.timeout}
              error={err('timeout_ms')}
              onChange={(e) => {
                set('timeout', e.target.value);
              }}
            />
          </div>
          {form.kind === 'http' && (
            <>
              <div className="grid gap-4 sm:grid-cols-2">
                <Field label={t('monitors.form.method')} error={err('settings.method')}>
                  {({ id }) => (
                    <select
                      id={id}
                      className={selectClass}
                      value={form.method}
                      onChange={(e) => {
                        set('method', e.target.value as MonitorForm['method']);
                      }}
                    >
                      <option value="GET">GET</option>
                      <option value="HEAD">HEAD</option>
                    </select>
                  )}
                </Field>
                <TextField
                  label={t('monitors.form.expectedStatus')}
                  hint={t('monitors.form.expectedStatusHint')}
                  autoComplete="off"
                  value={form.expectedStatus}
                  error={err('settings.expected_status')}
                  onChange={(e) => {
                    set('expectedStatus', e.target.value);
                  }}
                />
              </div>
              <TextField
                label={t('monitors.form.keyword')}
                hint={t('monitors.form.keywordHint')}
                autoComplete="off"
                value={form.keyword}
                error={err('settings.keyword')}
                onChange={(e) => {
                  set('keyword', e.target.value);
                }}
              />
            </>
          )}
          {form.kind === 'ssl' && (
            <TextField
              label={t('monitors.form.expiryDays')}
              inputMode="numeric"
              value={form.expiryDays}
              error={err('settings.expiry_days')}
              onChange={(e) => {
                set('expiryDays', e.target.value);
              }}
            />
          )}
          <TextField
            label={t('monitors.form.labels')}
            hint={t('monitors.form.labelsHint')}
            autoComplete="off"
            value={form.labels}
            error={err('labels')}
            onChange={(e) => {
              set('labels', e.target.value);
            }}
          />
          <div className="flex items-center gap-2">
            <Checkbox
              id={enabledId}
              checked={form.enabled}
              onCheckedChange={(c) => {
                set('enabled', c === true);
              }}
            />
            <Label htmlFor={enabledId} className="font-normal">
              {t('monitors.form.enabled')}
            </Label>
          </div>
          {hasOtherErrors(error, FIELDS) && <FormError error={error} />}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
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
