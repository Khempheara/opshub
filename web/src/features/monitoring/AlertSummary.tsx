import { useTranslation } from 'react-i18next';
import { useFormat } from '@/i18n/useFormat';
import type { Alert } from '@/lib/api/generated/model';
import { alertSummary } from './forms';

/** One sentence saying what the alert observed, in the reader's language. */
export function AlertSummary({ alert, className }: { alert: Alert; className?: string }) {
  const { t } = useTranslation('monitoring');
  const fmt = useFormat();
  const { key, values } = alertSummary(alert.rule_kind, alert.details, alert.subject_name);
  const shown = { ...values };
  if (typeof shown.value === 'number') shown.value = fmt.number(shown.value, { maximumFractionDigits: 1 });
  if (typeof shown.threshold === 'number') shown.threshold = fmt.number(shown.threshold, { maximumFractionDigits: 1 });
  return <span className={className}>{t(key, shown)}</span>;
}
