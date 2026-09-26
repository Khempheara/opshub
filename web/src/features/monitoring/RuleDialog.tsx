import { useQueryClient } from '@tanstack/react-query';
import { Plus, X } from 'lucide-react';
import { useId, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';
import { Field, TextField } from '@/components/common/Field';
import { FormError } from '@/components/common/States';
import { Button } from '@/components/ui/button';
import { Checkbox } from '@/components/ui/checkbox';
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog';
import { Label } from '@/components/ui/label';
import { selectClass } from '@/features/org/constants';
import { useListAssets } from '@/lib/api/generated/infrastructure/infrastructure';
import { AlertSeverity, type AlertRule } from '@/lib/api/generated/model';
import { createAlertRule, getListAlertRulesQueryKey, updateAlertRule, useListMonitors, useListNotificationChannels } from '@/lib/api/generated/monitoring/monitoring';
import { ifMatch } from '@/lib/api/idempotency';
import { defaultThreshold, emptyRuleForm, fieldError, hasOtherErrors, RULE_KINDS, ruleForm, ruleRequest, watchesMonitors, type RuleForm } from './forms';

const FIELDS = ['name', 'kind', 'target_id', 'label', 'threshold', 'metric', 'for_seconds', 'severity', 'escalation'];
const METRICS = ['cpu', 'mem', 'disk'] as const;

/** Create (rule undefined) or edit an alert rule. */
export function RuleDialog({ orgId, rule, onClose }: { orgId: string; rule?: AlertRule; onClose: () => void }) {
  const { t } = useTranslation(['monitoring', 'common']);
  const queryClient = useQueryClient();
  const enabledId = useId();
  const [form, setForm] = useState<RuleForm>(() => (rule ? ruleForm(rule) : emptyRuleForm()));
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const editing = rule !== undefined;
  const monitors = useListMonitors(orgId, undefined, { query: { enabled: watchesMonitors(form.kind) } });
  const assets = useListAssets(orgId, { kind: form.kind === 'certificate' ? 'domain' : 'server' }, { query: { enabled: !watchesMonitors(form.kind) } });
  const channels = useListNotificationChannels(orgId);
  const set = <K extends keyof RuleForm>(k: K, v: RuleForm[K]) => {
    setForm((f) => ({ ...f, [k]: v }));
  };
  const err = (name: string) => fieldError(error, name);
  const targets = watchesMonitors(form.kind)
    ? (monitors.data?.items ?? []).map((m) => ({ id: m.id, name: m.name }))
    : (assets.data?.items ?? []).map((a) => ({ id: a.id, name: a.name }));
  const thresholdKind = form.kind === 'monitor_latency' || form.kind === 'asset_metric' || form.kind === 'certificate' ? form.kind : null;
  const allTargets = watchesMonitors(form.kind) ? t('rules.allMonitors') : form.kind === 'certificate' ? t('rules.allDomains') : t('rules.allServers');

  const setStep = (i: number, patch: Partial<RuleForm['steps'][number]>) => {
    setForm((f) => ({ ...f, steps: f.steps.map((s, j) => (j === i ? { ...s, ...patch } : s)) }));
  };

  const submit = async () => {
    setBusy(true);
    setError(null);
    try {
      if (editing) {
        await updateAlertRule(rule.id, ruleRequest(form, false), ifMatch(rule.version));
        toast.success(t('rules.saved'));
      } else {
        await createAlertRule(orgId, ruleRequest(form, true));
        toast.success(t('rules.created'));
      }
      await queryClient.invalidateQueries({ queryKey: getListAlertRulesQueryKey(orgId) });
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
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{editing ? t('rules.editTitle', { name: rule.name }) : t('rules.createTitle')}</DialogTitle>
          <DialogDescription>{t('rules.intro')}</DialogDescription>
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
            label={t('rules.form.name')}
            autoComplete="off"
            value={form.name}
            error={err('name')}
            onChange={(e) => {
              set('name', e.target.value);
            }}
          />
          <Field label={t('rules.form.kind')} hint={t(`ruleKindHelp.${form.kind}`)} error={err('kind')}>
            {({ id, describedBy }) =>
              editing ? (
                <p id={id} className="py-1.5 text-sm">
                  {t(`ruleKinds.${form.kind}`)}
                </p>
              ) : (
                <select
                  id={id}
                  aria-describedby={describedBy}
                  className={selectClass}
                  value={form.kind}
                  onChange={(e) => {
                    const kind = e.target.value as RuleForm['kind'];
                    setForm((f) => ({ ...f, kind, targetId: '', threshold: defaultThreshold[kind] }));
                  }}
                >
                  {RULE_KINDS.map((k) => (
                    <option key={k} value={k}>
                      {t(`ruleKinds.${k}`)}
                    </option>
                  ))}
                </select>
              )
            }
          </Field>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label={t('rules.form.target')} error={err('target_id')}>
              {({ id }) => (
                <select
                  id={id}
                  className={selectClass}
                  value={form.targetId}
                  onChange={(e) => {
                    set('targetId', e.target.value);
                  }}
                >
                  <option value="">{allTargets}</option>
                  {targets.map((o) => (
                    <option key={o.id} value={o.id}>
                      {o.name}
                    </option>
                  ))}
                </select>
              )}
            </Field>
            <TextField
              label={t('rules.form.label')}
              hint={t('rules.form.labelHint')}
              autoComplete="off"
              value={form.label}
              error={err('label')}
              onChange={(e) => {
                set('label', e.target.value);
              }}
            />
          </div>
          <div className="grid gap-4 sm:grid-cols-3">
            {form.kind === 'asset_metric' && (
              <Field label={t('rules.form.metric')} error={err('metric')}>
                {({ id }) => (
                  <select
                    id={id}
                    className={selectClass}
                    value={form.metric}
                    onChange={(e) => {
                      set('metric', e.target.value as RuleForm['metric']);
                    }}
                  >
                    {METRICS.map((m) => (
                      <option key={m} value={m}>
                        {t(`metrics.${m}`)}
                      </option>
                    ))}
                  </select>
                )}
              </Field>
            )}
            {thresholdKind && (
              <TextField
                label={t(`rules.form.threshold.${thresholdKind}`)}
                inputMode="decimal"
                value={form.threshold}
                error={err('threshold')}
                onChange={(e) => {
                  set('threshold', e.target.value);
                }}
              />
            )}
            <TextField
              label={t('rules.form.forMinutes')}
              hint={t('rules.form.forHint')}
              inputMode="decimal"
              value={form.forMinutes}
              error={err('for_seconds')}
              onChange={(e) => {
                set('forMinutes', e.target.value);
              }}
            />
            <Field label={t('rules.form.severity')} error={err('severity')}>
              {({ id }) => (
                <select
                  id={id}
                  className={selectClass}
                  value={form.severity}
                  onChange={(e) => {
                    set('severity', e.target.value as RuleForm['severity']);
                  }}
                >
                  {Object.values(AlertSeverity).map((s) => (
                    <option key={s} value={s}>
                      {t(`severity.${s}`)}
                    </option>
                  ))}
                </select>
              )}
            </Field>
          </div>

          <fieldset className="space-y-3 rounded-md border p-3">
            <legend className="px-1 text-sm font-medium">{t('rules.form.escalation')}</legend>
            <p className="text-muted-foreground text-xs">{t('rules.form.escalationHint')}</p>
            {channels.data && channels.data.items.length === 0 && <p className="text-muted-foreground text-sm">{t('rules.form.noChannels')}</p>}
            {form.steps.map((step, i) => (
              <div key={i} className="space-y-2 border-t pt-3 first-of-type:border-t-0 first-of-type:pt-0" data-testid="escalation-step">
                <div className="flex flex-wrap items-end gap-3">
                  <p className="text-sm font-medium">{t('rules.form.step', { n: i + 1 })}</p>
                  {i > 0 && (
                    <TextField
                      className="w-36"
                      label={t('rules.form.afterMinutes')}
                      inputMode="numeric"
                      value={step.afterMinutes}
                      error={err(`escalation[${String(i)}].after_minutes`)}
                      onChange={(e) => {
                        setStep(i, { afterMinutes: e.target.value });
                      }}
                    />
                  )}
                  {i > 0 && (
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon"
                      aria-label={t('rules.form.removeStep', { n: i + 1 })}
                      onClick={() => {
                        setForm((f) => ({ ...f, steps: f.steps.filter((_, j) => j !== i) }));
                      }}
                    >
                      <X aria-hidden />
                    </Button>
                  )}
                </div>
                <div role="group" aria-label={`${t('rules.form.step', { n: i + 1 })}: ${t('rules.form.channels')}`} className="flex flex-wrap gap-x-4 gap-y-2">
                  {(channels.data?.items ?? []).map((c) => {
                    const id = `${enabledId}-s${String(i)}-${c.id}`;
                    return (
                      <div key={c.id} className="flex items-center gap-2">
                        <Checkbox
                          id={id}
                          checked={step.channelIds.includes(c.id)}
                          onCheckedChange={(v) => {
                            setStep(i, { channelIds: v === true ? [...step.channelIds, c.id] : step.channelIds.filter((x) => x !== c.id) });
                          }}
                        />
                        <Label htmlFor={id} className="font-normal">
                          {c.name} <span className="text-muted-foreground text-xs">({t(`channelKinds.${c.kind}`)})</span>
                        </Label>
                      </div>
                    );
                  })}
                </div>
                {err(`escalation[${String(i)}].channel_ids`) && (
                  <p role="alert" className="text-destructive text-sm">
                    {err(`escalation[${String(i)}].channel_ids`)}
                  </p>
                )}
              </div>
            ))}
            {form.steps.length < 5 && (channels.data?.items.length ?? 0) > 0 && (
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={() => {
                  const last = form.steps.at(-1);
                  const after = String((Number(last?.afterMinutes) || 0) + 15);
                  set('steps', [...form.steps, { afterMinutes: after, channelIds: [] }]);
                }}
              >
                <Plus aria-hidden />
                {t('rules.form.addStep')}
              </Button>
            )}
          </fieldset>

          <div className="flex items-center gap-2">
            <Checkbox
              id={enabledId}
              checked={form.enabled}
              onCheckedChange={(c) => {
                set('enabled', c === true);
              }}
            />
            <Label htmlFor={enabledId} className="font-normal">
              {t('rules.form.enabled')}
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
