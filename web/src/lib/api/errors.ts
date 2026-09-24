import type { FieldValues, Path, UseFormSetError } from 'react-hook-form';
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

/**
 * Maps VALIDATION_FAILED field errors onto a react-hook-form. Returns true when every
 * field error matched a form field (so no general error banner is needed).
 */
export function applyFieldErrors<T extends FieldValues>(err: unknown, setError: UseFormSetError<T>, fields: readonly Path<T>[]): boolean {
  if (!(err instanceof ApiError) || err.fieldErrors.length === 0) return false;
  let all = true;
  for (const fe of err.fieldErrors) {
    const name = fields.find((f) => f === fe.field);
    if (name) setError(name, { type: 'server', message: fieldErrorMessage(fe) });
    else all = false;
  }
  return all;
}

/** True when err is an ApiError with one of the given codes. */
export function hasCode(err: unknown, ...codes: string[]): err is ApiError {
  return err instanceof ApiError && codes.includes(err.code);
}
