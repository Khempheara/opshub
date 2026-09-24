import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { useDisplayPrefs } from '@/preferences/store';
import { formatDateTime, formatDuration, formatNumber, formatRelative, type FormatPrefs } from './format';
import { DEFAULT_LOCALE, isLocale } from './index';

/** Locale-, timezone- and numeral-aware formatters bound to the current preferences. */
export function useFormat() {
  const { i18n } = useTranslation();
  const { timeZone, khmerNumerals } = useDisplayPrefs();
  const lng = i18n.resolvedLanguage;
  const locale = isLocale(lng) ? lng : DEFAULT_LOCALE;

  return useMemo(() => {
    const prefs: FormatPrefs = { locale, timeZone, khmerNumerals };
    return {
      prefs,
      dateTime: (v: Date | string | number, o?: Intl.DateTimeFormatOptions) => formatDateTime(v, prefs, o),
      number: (v: number, o?: Intl.NumberFormatOptions) => formatNumber(v, prefs, o),
      relative: (v: Date | string | number) => formatRelative(v, prefs),
      duration: (ms: number) => formatDuration(ms, prefs),
    };
  }, [locale, timeZone, khmerNumerals]);
}
