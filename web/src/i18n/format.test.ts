import { describe, expect, it } from 'vitest';
import {
  formatDateTime,
  formatDuration,
  formatNumber,
  formatRelative,
  intlLocale,
  isValidTimeZone,
  toKhmerDigits,
  type FormatPrefs,
} from './format';

const en: FormatPrefs = { locale: 'en', timeZone: 'Asia/Phnom_Penh', khmerNumerals: false };
const km: FormatPrefs = { locale: 'km', timeZone: 'Asia/Phnom_Penh', khmerNumerals: false };
const kmDigits: FormatPrefs = { ...km, khmerNumerals: true };

const KHMER_DIGIT = /[០-៩]/;
const LATIN_DIGIT = /[0-9]/;

describe('intlLocale', () => {
  it('uses Khmer locale data when the engine has it, with Latin digits', () => {
    expect(intlLocale(km, true)).toBe('km-KH-u-nu-latn');
    expect(intlLocale(kmDigits, true)).toBe('km-KH-u-nu-latn');
    expect(intlLocale(en, true)).toBe('en-US');
  });

  it('falls back to English formatting on engines without Khmer data', () => {
    expect(intlLocale(km, false)).toBe('en-US');
  });
});

describe('toKhmerDigits', () => {
  it('transliterates ASCII digits only', () => {
    expect(toKhmerDigits('v1.2.30 at 10:09')).toBe('v១.២.៣០ at ១០:០៩');
    expect(toKhmerDigits('no digits')).toBe('no digits');
  });
});

describe('formatDateTime', () => {
  // 2026-01-15T17:30:00Z is 00:30 on Jan 16 in Phnom Penh (UTC+7).
  const instant = '2026-01-15T17:30:00Z';

  it('converts to the display time zone', () => {
    expect(formatDateTime(instant, en, { dateStyle: 'short', timeStyle: 'short' })).toBe('1/16/26, 12:30 AM');
    expect(formatDateTime(instant, { ...en, timeZone: 'UTC' }, { dateStyle: 'short', timeStyle: 'short' })).toBe(
      '1/15/26, 5:30 PM',
    );
  });

  it('uses Khmer numerals only when enabled', () => {
    const latin = formatDateTime(instant, km);
    const khmer = formatDateTime(instant, kmDigits);
    expect(latin).toMatch(LATIN_DIGIT);
    expect(latin).not.toMatch(KHMER_DIGIT);
    expect(khmer).toMatch(KHMER_DIGIT);
    expect(khmer).not.toMatch(LATIN_DIGIT);
  });
});

describe('formatNumber', () => {
  it('formats with locale digits', () => {
    expect(formatNumber(1234.5, en)).toBe('1,234.5');
    expect(formatNumber(1234, kmDigits)).toMatch(/^១.?២៣៤$/u);
    // Khmer numerals are a Khmer-UI preference; English stays Latin.
    expect(formatNumber(1234, { ...en, khmerNumerals: true })).toBe('1,234');
  });
});

describe('formatRelative', () => {
  it('picks the largest fitting unit', () => {
    const now = new Date('2026-01-01T12:00:00Z');
    expect(formatRelative('2026-01-01T11:57:00Z', en, now)).toBe('3 minutes ago');
    expect(formatRelative('2026-01-03T12:00:00Z', en, now)).toBe('in 2 days');
    expect(formatRelative('2026-01-01T11:57:00Z', kmDigits, now)).toMatch(KHMER_DIGIT);
  });
});

describe('formatDuration', () => {
  it('shows the two most significant units', () => {
    expect(formatDuration(5_000, en)).toBe('5s');
    expect(formatDuration(83_000, en)).toBe('1m 23s');
    expect(formatDuration(3_723_000, en)).toBe('1h 2m');
    expect(formatDuration(0, en)).toBe('0s');
    expect(formatDuration(83_000, kmDigits)).toMatch(KHMER_DIGIT);
  });
});

describe('isValidTimeZone', () => {
  it('accepts IANA names only', () => {
    expect(isValidTimeZone('Asia/Phnom_Penh')).toBe(true);
    expect(isValidTimeZone('UTC')).toBe(true);
    expect(isValidTimeZone('Mars/Olympus')).toBe(false);
  });
});

describe('Khmer fallback for engines without Khmer locale data', () => {
  const instant = '2026-09-24T03:05:00Z'; // Thursday 10:05 in Phnom Penh

  it('uses Khmer month, weekday and day-period names', () => {
    const out = formatDateTime(instant, km, { dateStyle: 'full', timeStyle: 'short' }, false);
    expect(out).toContain('ព្រហស្បតិ៍');
    expect(out).toContain('កញ្ញា');
    expect(out).toContain('ព្រឹក');
    expect(out).not.toMatch(/Thursday|September|AM/);
    expect(formatDateTime(instant, kmDigits, { dateStyle: 'medium' }, false)).toMatch(/^កញ្ញា ២៤, ២០២៦$/);
  });

  it('formats relative times and durations in Khmer', () => {
    const now = new Date('2026-01-01T12:00:00Z');
    expect(formatRelative('2026-01-01T11:57:00Z', km, now, false)).toBe('3 នាទីមុន');
    expect(formatRelative('2026-01-01T11:57:00Z', kmDigits, now, false)).toBe('៣ នាទីមុន');
    expect(formatRelative('2026-01-03T12:00:00Z', km, now, false)).toBe('ក្នុងរយៈពេល 2 ថ្ងៃ');
    expect(formatRelative('2026-01-01T11:59:58Z', km, now, false)).toBe('ឥឡូវនេះ');
    expect(formatDuration(83_000, kmDigits, false)).toBe('១ នាទី ២៣ វិនាទី');
  });

  it('leaves English untouched', () => {
    expect(formatRelative('2026-01-01T11:57:00Z', en, new Date('2026-01-01T12:00:00Z'), false)).toBe('3 minutes ago');
  });
});
