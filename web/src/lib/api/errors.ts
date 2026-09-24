import i18n from '@/i18n';
import { ApiError, NetworkError, type FieldError } from './fetcher';

// Uses the i18n instance directly so callers needn't pass a namespace-scoped t();
// components still re-render on language change through their own useTranslation().
const tErrors = () => i18n.getFixedT(null, 'errors');

/** Localized, user-facing message for any error thrown by the API layer. */
export function errorMessage(err: unknown): string {
  const t = tErrors();
  if (err instanceof NetworkError) return t('network');
  if (err instanceof ApiError) {
    return t(`codes.${err.code}` as 'codes.INTERNAL', { defaultValue: t('fallback') });
  }
  return t('fallback');
}

/** Localized message for one VALIDATION_FAILED field error. */
export function fieldErrorMessage(fe: FieldError): string {
  const t = tErrors();
  return t(`rules.${fe.rule}` as 'rules.min', { param: fe.param ?? '', defaultValue: t('rules.default') });
}
