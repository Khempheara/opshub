import { useSyncExternalStore } from 'react';
import type { TokenResponse, User } from '@/lib/api/generated/model';

/**
 * Authentication state.
 *
 * - The 15-minute access token lives only in memory (never localStorage/sessionStorage),
 *   so an XSS bug cannot exfiltrate a long-lived credential.
 * - The refresh token is an httpOnly cookie the browser sends to /api/v1/auth/refresh.
 * - Refresh tokens rotate and reuse is treated as theft by the server, so refreshes are
 *   serialized across tabs with the Web Locks API (the second tab then uses the rotated
 *   cookie instead of replaying the old one).
 */
export type SessionStatus = 'loading' | 'authenticated' | 'anonymous';

interface State {
  status: SessionStatus;
  user: User | null;
  /** Why the last session ended: a deliberate sign-out doesn't remember the page. */
  endedBy?: 'signout';
}

let state: State = { status: 'loading', user: null };
let accessToken: string | null = null;
let refreshTimer: ReturnType<typeof setTimeout> | undefined;
let inflight: Promise<boolean> | null = null;
const listeners = new Set<() => void>();

const channel = typeof BroadcastChannel !== 'undefined' ? new BroadcastChannel('opshub-auth') : null;

function emit(next: State) {
  state = next;
  listeners.forEach((l) => {
    l();
  });
}

export function getAccessToken(): string | null {
  return accessToken;
}

export function getSession(): State {
  return state;
}

/** Store a freshly issued access token and schedule the next silent refresh. */
export function setSession(tokens: TokenResponse): void {
  accessToken = tokens.access_token;
  clearTimeout(refreshTimer);
  const msUntilExpiry = new Date(tokens.expires_at).getTime() - Date.now();
  refreshTimer = setTimeout(() => void refreshSession(), Math.max(msUntilExpiry - 60_000, 5_000));
  emit({ status: 'authenticated', user: tokens.user });
}

/** Replace the cached user (after a profile update) without touching tokens. */
export function updateSessionUser(user: User): void {
  if (state.status === 'authenticated') emit({ status: 'authenticated', user });
}

/**
 * Forget the session locally. `signOut` marks a deliberate sign-out (also broadcast to other
 * tabs); otherwise the session simply expired and sign-in returns to the current page.
 */
export function clearSession(signOut = false): void {
  accessToken = null;
  clearTimeout(refreshTimer);
  emit(signOut ? { status: 'anonymous', user: null, endedBy: 'signout' } : { status: 'anonymous', user: null });
  if (signOut) channel?.postMessage('logout');
}

export function readCookie(name: string): string | null {
  const match = document.cookie.split('; ').find((c) => c.startsWith(name + '='));
  return match ? decodeURIComponent(match.slice(name.length + 1)) : null;
}

export const CSRF_COOKIE = 'opshub_csrf';
export const CSRF_HEADER = 'X-CSRF-Token';

async function doRefresh(): Promise<boolean> {
  const csrf = readCookie(CSRF_COOKIE);
  if (!csrf) {
    clearSession();
    return false;
  }
  try {
    const res = await fetch('/api/v1/auth/refresh', {
      method: 'POST',
      credentials: 'same-origin',
      headers: { [CSRF_HEADER]: csrf, Accept: 'application/json' },
    });
    if (!res.ok) {
      clearSession();
      return false;
    }
    setSession((await res.json()) as TokenResponse);
    return true;
  } catch {
    // Network error: keep the current state; the next request will retry.
    if (state.status === 'loading') emit({ status: 'anonymous', user: null });
    return false;
  }
}

/**
 * Rotate the refresh cookie for a new access token. Concurrent callers in this tab share
 * one request; other tabs wait on the same Web Lock.
 */
export function refreshSession(): Promise<boolean> {
  inflight ??= (async () => {
    try {
      if (typeof navigator !== 'undefined' && 'locks' in navigator) {
        return await navigator.locks.request('opshub-refresh', doRefresh);
      }
      return await doRefresh();
    } finally {
      inflight = null;
    }
  })();
  return inflight;
}

/** Restore the session on page load (silent refresh from the cookie). */
export async function bootstrapSession(): Promise<void> {
  if (!readCookie(CSRF_COOKIE)) {
    emit({ status: 'anonymous', user: null });
    return;
  }
  await refreshSession();
}

channel?.addEventListener('message', (e: MessageEvent) => {
  if (e.data === 'logout') {
    accessToken = null;
    clearTimeout(refreshTimer);
    emit({ status: 'anonymous', user: null, endedBy: 'signout' });
  }
  if (e.data === 'login' && state.status !== 'authenticated') void refreshSession();
});

/** Tell other tabs a sign-in happened so they pick up the session. */
export function broadcastLogin(): void {
  channel?.postMessage('login');
}

function subscribe(l: () => void) {
  listeners.add(l);
  return () => listeners.delete(l);
}

export function useSession(): State {
  return useSyncExternalStore(subscribe, getSession, getSession);
}
