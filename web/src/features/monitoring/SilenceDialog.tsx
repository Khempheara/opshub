import { useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';
import { Field, TextField } from '@/components/common/Field';
import { FormError } from '@/components/common/States';
import { Button } from '@/components/ui/button';
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog';
import { selectClass } from '@/features/org/constants';
import { AlertSeverity, type AlertRule } from '@/lib/api/generated/model';
import { createSilence, getListAlertsQueryKey, getListSilencesQueryKey } from '@/lib/api/generated/monitoring/monitoring';
import { fieldError, hasOtherErrors } from './forms';

const DURATIONS = { '1h': 1, '4h': 4, '24h': 24, '7d': 24 * 7 } as const;
type DurationKey = keyof typeof DURATIONS;
const FIELDS = ['rule_id', 'subject_id', 'label', 'severity', 'comment', 'ends_at', 'matchers'];

/**
 * Creates a silence. With subject set (from an alert), it silences that rule on that subject
 * and only the duration and comment are asked.
 */
export function SilenceDialog({
  orgId,
  rules,
  subject,
  onClose,
}: {
  orgId: string;
  rules: AlertRule[];
  subject?: { ruleId: string | null; id: string; name: string };
  onClose: () => void;
}) {
  const { t } = useTranslation(['monitoring', 'common']);
  const queryClient = useQueryClient();
  const [ruleId, setRuleId] = useState(subject?.ruleId ?? '');
  const [severity, setSeverity] = useState('');
  const [label, setLabel] = useState('');
  const [duration, setDuration] = useState<DurationKey>('1h');
  const [comment, setComment] = useState('');
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  // "Set at least one filter" belongs to no single field.
  const matchersError = fieldError(error, 'matchers');

  const submit = async () => {
    setBusy(true);
    setError(null);
    try {
      await createSilence(orgId, {
        rule_id: ruleId || null,
        subject_id: subject?.id ?? null,
        severity: (severity || null) as AlertSeverity | null,
        label: label.trim() || null,
        comment: comment.trim(),
        ends_at: new Date(Date.now() + DURATIONS[duration] * 3600_000).toISOString(),
      });
      await queryClient.invalidateQueries({ queryKey: getListSilencesQueryKey(orgId) });
      await queryClient.invalidateQueries({ queryKey: getListAlertsQueryKey(orgId) });
      toast.success(t('silences.created'));
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
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{subject ? t('alerts.silenceTitle') : t('silences.createTitle')}</DialogTitle>
          <DialogDescription>{subject ? t('alerts.silenceIntro', { subject: subject.name }) : t('silences.intro')}</DialogDescription>
        </DialogHeader>
        <form
          noValidate
          className="space-y-4"
          onSubmit={(e) => {
            e.preventDefault();
            void submit();
          }}
        >
          {!subject && (
            <>
              <Field label={t('silences.form.rule')} error={fieldError(error, 'rule_id')}>
                {({ id }) => (
                  <select
                    id={id}
                    className={selectClass}
                    value={ruleId}
                    onChange={(e) => {
                      setRuleId(e.target.value);
                    }}
                  >
                    <option value="">{t('silences.form.anyRule')}</option>
                    {rules.map((r) => (
                      <option key={r.id} value={r.id}>
                        {r.name}
                      </option>
                    ))}
                  </select>
                )}
              </Field>
              <div className="grid gap-4 sm:grid-cols-2">
                <Field label={t('silences.form.severity')} error={fieldError(error, 'severity')}>
                  {({ id }) => (
                    <select
                      id={id}
                      className={selectClass}
                      value={severity}
                      onChange={(e) => {
                        setSeverity(e.target.value);
                      }}
                    >
                      <option value="">{t('silences.form.anySeverity')}</option>
                      {Object.values(AlertSeverity).map((s) => (
                        <option key={s} value={s}>
                          {t(`severity.${s}`)}
                        </option>
                      ))}
                    </select>
                  )}
                </Field>
                <TextField
                  label={t('silences.form.label')}
                  hint={t('silences.form.labelHint')}
                  autoComplete="off"
                  value={label}
                  error={fieldError(error, 'label')}
                  onChange={(e) => {
                    setLabel(e.target.value);
                  }}
                />
              </div>
            </>
          )}
          <Field label={t('silences.form.duration')} error={fieldError(error, 'ends_at')}>
            {({ id }) => (
              <select
                id={id}
                className={selectClass}
                value={duration}
                onChange={(e) => {
                  setDuration(e.target.value as DurationKey);
                }}
              >
                {(Object.keys(DURATIONS) as DurationKey[]).map((d) => (
                  <option key={d} value={d}>
                    {t(`silences.form.durations.${d}`)}
                  </option>
                ))}
              </select>
            )}
          </Field>
          <TextField
            label={t('silences.form.comment')}
            hint={t('silences.form.commentHint')}
            autoComplete="off"
            value={comment}
            error={fieldError(error, 'comment')}
            onChange={(e) => {
              setComment(e.target.value);
            }}
          />
          {matchersError && (
            <p role="alert" className="text-destructive text-sm">
              {t('silences.needMatcher')}
            </p>
          )}
          {!matchersError && hasOtherErrors(error, FIELDS) && <FormError error={error} />}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              {t('common:actions.cancel')}
            </Button>
            <Button type="submit" disabled={busy}>
              {t('common:actions.create')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
