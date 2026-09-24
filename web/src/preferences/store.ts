import { useSyncExternalStore } from 'react';
import { isValidTimeZone } from '@/i18n/format';

/**
 * Display preferences that are not part of i18next (which owns the language).
 * Persisted in localStorage until the user signs in; after sign-in the values from
 * the user's profile (users.timezone / users.khmer_numerals) take precedence.
 */
export interface DisplayPrefs {
  timeZone: string;
  khmerNumerals: boolean;
}

export const DEFAULT_TIMEZONE = 'Asia/Phnom_Penh';
const STORAGE_KEY = 'opshub.displayPrefs';
const DEFAULTS: DisplayPrefs = { timeZone: DEFAULT_TIMEZONE, khmerNumerals: false };

function load(): DisplayPrefs {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) return DEFAULTS;
    const parsed = JSON.parse(raw) as Partial<DisplayPrefs>;
    return {
      timeZone:
        typeof parsed.timeZone === 'string' && isValidTimeZone(parsed.timeZone) ? parsed.timeZone : DEFAULTS.timeZone,
      khmerNumerals: typeof parsed.khmerNumerals === 'boolean' ? parsed.khmerNumerals : DEFAULTS.khmerNumerals,
    };
  } catch {
    return DEFAULTS;
  }
}

let state = load();
const listeners = new Set<() => void>();

export function getDisplayPrefs(): DisplayPrefs {
  return state;
}

export function setDisplayPrefs(next: Partial<DisplayPrefs>): void {
  state = { ...state, ...next };
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(state));
  } catch {
    // Storage may be unavailable (private mode); keep the in-memory value.
  }
  listeners.forEach((l) => {
    l();
  });
}

function subscribe(listener: () => void) {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

export function useDisplayPrefs(): DisplayPrefs {
  return useSyncExternalStore(subscribe, getDisplayPrefs, getDisplayPrefs);
}
