import { useSyncExternalStore } from 'react';

export type ThemePreference = 'light' | 'dark' | 'system';
const STORAGE_KEY = 'opshub.theme';

function read(): ThemePreference {
  try {
    const v = localStorage.getItem(STORAGE_KEY);
    return v === 'light' || v === 'dark' ? v : 'system';
  } catch {
    return 'system';
  }
}

const media = typeof window !== 'undefined' ? window.matchMedia('(prefers-color-scheme: dark)') : null;
let preference = read();
const listeners = new Set<() => void>();

function resolve(p: ThemePreference): 'light' | 'dark' {
  if (p === 'system') return media?.matches ? 'dark' : 'light';
  return p;
}

function apply() {
  document.documentElement.classList.toggle('dark', resolve(preference) === 'dark');
  listeners.forEach((l) => {
    l();
  });
}

media?.addEventListener('change', () => {
  if (preference === 'system') apply();
});

export function setTheme(p: ThemePreference): void {
  preference = p;
  try {
    localStorage.setItem(STORAGE_KEY, p);
  } catch {
    // keep in memory only
  }
  apply();
}

let snapshot = { theme: preference, resolved: resolve(preference) };
function getSnapshot() {
  const resolved = resolve(preference);
  if (snapshot.theme !== preference || snapshot.resolved !== resolved) snapshot = { theme: preference, resolved };
  return snapshot;
}

function subscribe(l: () => void) {
  listeners.add(l);
  return () => listeners.delete(l);
}

/** Current theme preference and the resolved light/dark value. */
export function useTheme() {
  const s = useSyncExternalStore(subscribe, getSnapshot, getSnapshot);
  return { ...s, setTheme };
}
