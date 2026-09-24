import type { Locale } from './index';

export interface FormatPrefs {
  locale: Locale;
  timeZone: string;
  /** Render digits as ០១២៣… Only applies when locale is Khmer. */
  khmerNumerals: boolean;
}

/**
 * Whether this JS engine ships Khmer locale data. Mainstream browsers do, but some
 * embedded/trimmed Chromium builds and webviews don't — they silently format in English
 * and ignore `-u-nu-khmr`.
 */
export const HAS_NATIVE_KHMER = (() => {
  try {
    return Intl.DateTimeFormat.supportedLocalesOf(['km']).length > 0;
  } catch {
    return false;
  }
})();

/**
 * BCP-47 tag for Intl. Digits are always Latin here (`-u-nu-latn`); Khmer numerals are
 * applied by {@link toKhmerDigits} so they work even without engine locale data.
 */
export function intlLocale({ locale }: FormatPrefs, nativeKhmer = HAS_NATIVE_KHMER): string {
  return locale === 'km' && nativeKhmer ? 'km-KH-u-nu-latn' : 'en-US';
}

const KHMER_DIGITS = '០១២៣៤៥៦៧៨៩';

/** 2026 → ២០២៦. Only ASCII digits are replaced. */
export function toKhmerDigits(s: string): string {
  return s.replace(/[0-9]/g, (d) => KHMER_DIGITS.charAt(Number(d)));
}

/** Applies the Khmer-numerals preference (Khmer UI only). */
function digits(s: string, prefs: FormatPrefs): string {
  return prefs.locale === 'km' && prefs.khmerNumerals ? toKhmerDigits(s) : s;
}

export function isValidTimeZone(tz: string): boolean {
  try {
    new Intl.DateTimeFormat('en-US', { timeZone: tz });
    return true;
  } catch {
    return false;
  }
}

// Intl formatters are expensive to construct; cache by options.
const cache = new Map<string, Intl.DateTimeFormat | Intl.NumberFormat | Intl.RelativeTimeFormat>();
function cached<T extends Intl.DateTimeFormat | Intl.NumberFormat | Intl.RelativeTimeFormat>(
  key: string,
  make: () => T,
): T {
  let f = cache.get(key) as T | undefined;
  if (!f) {
    f = make();
    cache.set(key, f);
  }
  return f;
}

export function formatDateTime(
  value: Date | string | number,
  prefs: FormatPrefs,
  options: Intl.DateTimeFormatOptions = { dateStyle: 'medium', timeStyle: 'short' },
): string {
  const tag = intlLocale(prefs);
  const f = cached(`dt|${tag}|${prefs.timeZone}|${JSON.stringify(options)}`, () =>
    new Intl.DateTimeFormat(tag, { ...options, timeZone: prefs.timeZone }),
  );
  return digits(f.format(new Date(value)), prefs);
}

export function formatNumber(value: number, prefs: FormatPrefs, options: Intl.NumberFormatOptions = {}): string {
  const tag = intlLocale(prefs);
  const f = cached(`num|${tag}|${JSON.stringify(options)}`, () => new Intl.NumberFormat(tag, options));
  return digits(f.format(value), prefs);
}

const RELATIVE_UNITS: [Intl.RelativeTimeFormatUnit, number][] = [
  ['year', 365 * 24 * 3600],
  ['month', 30 * 24 * 3600],
  ['week', 7 * 24 * 3600],
  ['day', 24 * 3600],
  ['hour', 3600],
  ['minute', 60],
  ['second', 1],
];

/** "3 minutes ago" / "៣ នាទីមុន". */
export function formatRelative(value: Date | string | number, prefs: FormatPrefs, now: Date = new Date()): string {
  const tag = intlLocale(prefs);
  const f = cached(`rel|${tag}`, () => new Intl.RelativeTimeFormat(tag, { numeric: 'auto' }));
  const diffSec = Math.round((new Date(value).getTime() - now.getTime()) / 1000);
  for (const [unit, secs] of RELATIVE_UNITS) {
    if (Math.abs(diffSec) >= secs || unit === 'second') {
      return digits(f.format(Math.round(diffSec / secs), unit), prefs);
    }
  }
  return '';
}

/**
 * Compact duration for build/deploy timings: the two most significant units,
 * with CLDR-localized unit labels ("1h 5m", "1 ម៉ោង 5 នាទី").
 */
export function formatDuration(ms: number, prefs: FormatPrefs): string {
  const totalSec = Math.max(0, Math.round(ms / 1000));
  const parts: [number, 'hour' | 'minute' | 'second'][] = [
    [Math.floor(totalSec / 3600), 'hour'],
    [Math.floor((totalSec % 3600) / 60), 'minute'],
    [totalSec % 60, 'second'],
  ];
  const first = parts.findIndex(([v]) => v > 0);
  const shown = first === -1 ? parts.slice(2) : parts.slice(first, first + 2);
  return shown
    .map(([v, unit]) => formatNumber(v, prefs, { style: 'unit', unit, unitDisplay: 'narrow' }))
    .join(' ');
}
