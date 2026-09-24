import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { TokenResponse } from '@/lib/api/generated/model';
import { bootstrapSession, clearSession, getAccessToken, getSession, refreshSession, setSession } from './session';

const user = {
  id: 'u1', email: 'dara@example.com', display_name: 'Dara', locale: 'km', timezone: 'Asia/Phnom_Penh',
  khmer_numerals: false, email_verified: true, two_factor_enabled: false, has_password: true,
  is_platform_admin: false, created_at: '2026-01-01T00:00:00Z', version: 1,
} as const;

const tokens = (access: string): TokenResponse => ({
  access_token: access, token_type: 'Bearer', expires_in: 900,
  expires_at: new Date(Date.now() + 900_000).toISOString(), user: { ...user },
});

function setCSRFCookie(value: string | null) {
  document.cookie = value ? `opshub_csrf=${value}; path=/` : 'opshub_csrf=; expires=Thu, 01 Jan 1970 00:00:00 GMT; path=/';
}

beforeEach(() => {
  clearSession();
});
afterEach(() => {
  vi.restoreAllMocks();
  setCSRFCookie(null);
});

describe('session', () => {
  it('keeps the access token in memory only', () => {
    setSession(tokens('abc'));
    expect(getAccessToken()).toBe('abc');
    expect(getSession()).toMatchObject({ status: 'authenticated', user: { email: 'dara@example.com' } });
    expect(JSON.stringify(localStorage)).not.toContain('abc');
    expect(JSON.stringify(sessionStorage)).not.toContain('abc');
  });

  it('is anonymous on boot without a CSRF cookie (no request made)', async () => {
    const spy = vi.spyOn(globalThis, 'fetch');
    await bootstrapSession();
    expect(getSession().status).toBe('anonymous');
    expect(spy).not.toHaveBeenCalled();
  });

  it('restores the session from the refresh cookie, sending the CSRF header', async () => {
    setCSRFCookie('csrf-1');
    const spy = vi.spyOn(globalThis, 'fetch').mockResolvedValue(Response.json(tokens('fresh')));
    await bootstrapSession();
    expect(getAccessToken()).toBe('fresh');
    const init = spy.mock.calls[0]?.[1];
    expect(new Headers(init?.headers).get('X-CSRF-Token')).toBe('csrf-1');
    expect(init?.credentials).toBe('same-origin');
  });

  it('shares one refresh between concurrent callers (refresh tokens are single-use)', async () => {
    setCSRFCookie('csrf-1');
    let resolve!: (r: Response) => void;
    const spy = vi.spyOn(globalThis, 'fetch').mockReturnValue(new Promise((r) => (resolve = r)));
    const a = refreshSession();
    const b = refreshSession();
    resolve(Response.json(tokens('shared')));
    expect(await Promise.all([a, b])).toEqual([true, true]);
    expect(spy).toHaveBeenCalledTimes(1);
  });

  it('signs out when the refresh is rejected', async () => {
    setCSRFCookie('csrf-1');
    setSession(tokens('old'));
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      Response.json({ error: { code: 'REFRESH_TOKEN_REUSED', message: '', details: {} } }, { status: 401 }),
    );
    expect(await refreshSession()).toBe(false);
    expect(getSession().status).toBe('anonymous');
    expect(getAccessToken()).toBeNull();
  });
});
