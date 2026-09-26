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

/*
 * Khmer locale data (from CLDR) used only when the JS engine has no Khmer ICU data. Mainstream
 * browsers format Khmer natively; embedded/headless Chromium builds and some webviews don't.
 * These tables are locale data, not UI copy (UI strings live in src/locales).
 */
const KM_MONTHS = ['មករា', 'កុម្ភៈ', 'មីនា', 'មេសា', 'ឧសភា', 'មិថុនា', 'កក្កដា', 'សីហា', 'កញ្ញា', 'តុលា', 'វិច្ឆិកា', 'ធ្នូ'];
const KM_WEEKDAYS = ['អាទិត្យ', 'ច័ន្ទ', 'អង្គារ', 'ពុធ', 'ព្រហស្បតិ៍', 'សុក្រ', 'សៅរ៍'];
const KM_UNITS: Record<Intl.RelativeTimeFormatUnit, string> = {
  year: 'ឆ្នាំ', years: 'ឆ្នាំ', quarter: 'ត្រីមាស', quarters: 'ត្រីមាស', month: 'ខែ', months: 'ខែ',
  week: 'សប្តាហ៍', weeks: 'សប្តាហ៍', day: 'ថ្ងៃ', days: 'ថ្ងៃ', hour: 'ម៉ោង', hours: 'ម៉ោង',
  minute: 'នាទី', minutes: 'នាទី', second: 'វិនាទី', seconds: 'វិនាទី',
};

/** Whether Khmer must be produced by the fallback tables for these prefs. */
function needsKhmerFallback(prefs: FormatPrefs, nativeKhmer = HAS_NATIVE_KHMER): boolean {
  return prefs.locale === 'km' && !nativeKhmer;
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
  nativeKhmer = HAS_NATIVE_KHMER,
): string {
  const date = new Date(value);
  const tag = intlLocale(prefs, nativeKhmer);
  const f = cached(`dt|${tag}|${prefs.timeZone}|${JSON.stringify(options)}`, () =>
    new Intl.DateTimeFormat(tag, { ...options, timeZone: prefs.timeZone }),
  );
  if (!needsKhmerFallback(prefs, nativeKhmer)) return digits(f.format(date), prefs);

  // Fallback: English layout with Khmer month, weekday and day-period names.
  const numeric = cached(`dtn|${prefs.timeZone}`, () =>
    new Intl.DateTimeFormat('en-US', { month: 'numeric', weekday: 'short', timeZone: prefs.timeZone }),
  ).formatToParts(date);
  const monthIndex = Number(numeric.find((p) => p.type === 'month')?.value ?? '1') - 1;
  const weekdayIndex = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'].indexOf(numeric.find((p) => p.type === 'weekday')?.value ?? '');
  const out = f
    .formatToParts(date)
    .map((p) => {
      if (p.type === 'month' && !/^\d+$/.test(p.value)) return KM_MONTHS[monthIndex] ?? p.value;
      if (p.type === 'weekday') return KM_WEEKDAYS[weekdayIndex] ?? p.value;
      if (p.type === 'dayPeriod') return p.value.toUpperCase().startsWith('A') ? 'ព្រឹក' : 'ល្ងាច';
      return p.value;
    })
    .join('');
  return digits(out, prefs);
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
export function formatRelative(
  value: Date | string | number,
  prefs: FormatPrefs,
  now: Date = new Date(),
  nativeKhmer = HAS_NATIVE_KHMER,
): string {
  const tag = intlLocale(prefs, nativeKhmer);
  const f = cached(`rel|${tag}`, () => new Intl.RelativeTimeFormat(tag, { numeric: 'auto' }));
  const diffSec = Math.round((new Date(value).getTime() - now.getTime()) / 1000);
  for (const [unit, secs] of RELATIVE_UNITS) {
    if (Math.abs(diffSec) >= secs || unit === 'second') {
      const n = Math.round(diffSec / secs);
      if (!needsKhmerFallback(prefs, nativeKhmer)) return digits(f.format(n, unit), prefs);
      if (unit === 'second' && Math.abs(n) < 10) return 'ឥឡូវនេះ';
      const amount = digits(String(Math.abs(n)), prefs);
      return n < 0 ? `${amount} ${KM_UNITS[unit]}មុន` : `ក្នុងរយៈពេល ${amount} ${KM_UNITS[unit]}`;
    }
  }
  return '';
}

/**
 * Compact duration for build/deploy timings: the two most significant units,
 * with CLDR-localized unit labels ("1h 5m", "1 ម៉ោង 5 នាទី").
 */
export function formatDuration(ms: number, prefs: FormatPrefs, nativeKhmer = HAS_NATIVE_KHMER): string {
  const totalSec = Math.max(0, Math.round(ms / 1000));
  const parts: [number, 'day' | 'hour' | 'minute' | 'second'][] = [
    [Math.floor(totalSec / 86400), 'day'],
    [Math.floor((totalSec % 86400) / 3600), 'hour'],
    [Math.floor((totalSec % 3600) / 60), 'minute'],
    [totalSec % 60, 'second'],
  ];
  const first = parts.findIndex(([v]) => v > 0);
  const shown = first === -1 ? parts.slice(3) : parts.slice(first, first + 2);
  if (needsKhmerFallback(prefs, nativeKhmer)) {
    return shown.map(([v, unit]) => `${digits(String(v), prefs)} ${KM_UNITS[unit]}`).join(' ');
  }
  return shown
    .map(([v, unit]) => formatNumber(v, prefs, { style: 'unit', unit, unitDisplay: 'narrow' }))
    .join(' ');
}
