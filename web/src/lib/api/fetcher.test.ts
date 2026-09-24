import i18n from 'i18next';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { clearSession, getSession, setSession } from '@/auth/session';
import { applyFieldErrors, errorMessage, fieldErrorMessage } from './errors';
import { ApiError, customFetch, NetworkError } from './fetcher';

const user = {
  id: 'u1', email: 'a@b.co', display_name: 'A', locale: 'en', timezone: 'UTC', khmer_numerals: false,
  email_verified: true, two_factor_enabled: false, has_password: true, is_platform_admin: false,
  created_at: '2026-01-01T00:00:00Z', version: 1,
} as const;
const tokens = (access: string) => ({
  access_token: access, token_type: 'Bearer' as const, expires_in: 900,
  expires_at: new Date(Date.now() + 900_000).toISOString(), user: { ...user },
});
const errorResponse = (status: number, code: string, details = {}) =>
  Response.json({ error: { code, message: code, details } }, { status });

function mockFetch(impl: (url: string, init?: RequestInit) => Promise<Response>) {
  return vi.spyOn(globalThis, 'fetch').mockImplementation((input, init) => impl(input instanceof Request ? input.url : input.toString(), init));
}

beforeEach(() => {
  clearSession();
});
afterEach(async () => {
  vi.restoreAllMocks();
  document.cookie = 'opshub_csrf=; expires=Thu, 01 Jan 1970 00:00:00 GMT; path=/';
  await i18n.changeLanguage('en');
});

describe('customFetch', () => {
  it('sends the bearer token and the active language', async () => {
    setSession(tokens('tok-1'));
    await i18n.changeLanguage('km');
    const spy = mockFetch(() => Promise.resolve(Response.json({ ok: true })));
    await expect(customFetch('/api/v1/me')).resolves.toEqual({ ok: true });
    const headers = new Headers(spy.mock.calls[0]?.[1]?.headers);
    expect(headers.get('Authorization')).toBe('Bearer tok-1');
    expect(headers.get('Accept-Language')).toBe('km');
  });

  it('refreshes once on 401 and retries with the new token', async () => {
    setSession(tokens('expired'));
    document.cookie = 'opshub_csrf=c1; path=/';
    const seen: string[] = [];
    mockFetch((url, init) => {
      if (url === '/api/v1/auth/refresh') return Promise.resolve(Response.json(tokens('renewed')));
      seen.push(new Headers(init?.headers).get('Authorization') ?? '');
      return Promise.resolve(seen.length === 1 ? errorResponse(401, 'UNAUTHENTICATED') : Response.json({ ok: 1 }));
    });
    await expect(customFetch('/api/v1/me')).resolves.toEqual({ ok: 1 });
    expect(seen).toEqual(['Bearer expired', 'Bearer renewed']);
  });

  it('signs out when the refresh fails', async () => {
    setSession(tokens('expired'));
    document.cookie = 'opshub_csrf=c1; path=/';
    mockFetch((url) =>
      Promise.resolve(url === '/api/v1/auth/refresh' ? errorResponse(401, 'REFRESH_TOKEN_INVALID') : errorResponse(401, 'UNAUTHENTICATED')),
    );
    await expect(customFetch('/api/v1/me')).rejects.toMatchObject({ code: 'UNAUTHENTICATED' });
    expect(getSession().status).toBe('anonymous');
  });

  it('never retries auth endpoints (a bad password is not an expired session)', async () => {
    setSession(tokens('t'));
    const spy = mockFetch(() => Promise.resolve(errorResponse(401, 'INVALID_CREDENTIALS')));
    await expect(customFetch('/api/v1/auth/login', { method: 'POST' })).rejects.toMatchObject({ code: 'INVALID_CREDENTIALS' });
    expect(spy).toHaveBeenCalledTimes(1);
  });

  it('maps the error envelope, non-JSON pages and network failures', async () => {
    mockFetch(() => Promise.resolve(errorResponse(423, 'ACCOUNT_LOCKED', { retry_after_seconds: 600 })));
    const locked = (await customFetch('/api/v1/auth/login').catch((e: unknown) => e)) as ApiError;
    expect(locked).toBeInstanceOf(ApiError);
    expect(locked.retryAfterSeconds).toBe(600);

    vi.restoreAllMocks();
    mockFetch(() => Promise.resolve(new Response('<html>Bad Gateway</html>', { status: 502 })));
    await expect(customFetch('/api/v1/x')).rejects.toMatchObject({ code: 'INTERNAL', status: 502 });

    vi.restoreAllMocks();
    mockFetch(() => Promise.reject(new TypeError('Failed to fetch')));
    await expect(customFetch('/healthz')).rejects.toBeInstanceOf(NetworkError);
  });
});

describe('error translation', () => {
  it('translates by code, in the active language', async () => {
    const err = new ApiError(403, 'FORBIDDEN', 'you do not have permission');
    expect(errorMessage(err)).toBe("You don't have permission to do this.");
    await i18n.changeLanguage('km');
    expect(errorMessage(err)).toBe('អ្នកមិនមានសិទ្ធិធ្វើសកម្មភាពនេះទេ។');
    expect(errorMessage(new ApiError(401, 'INVALID_CREDENTIALS', ''))).toBe('អ៊ីមែល ឬពាក្យសម្ងាត់មិនត្រឹមត្រូវ។');
  });

  it('falls back for unknown codes and non-API errors', () => {
    expect(errorMessage(new ApiError(418, 'NEW_CODE_FROM_NEWER_SERVER', ''))).toBe('Something went wrong. Please try again.');
    expect(errorMessage(new Error('boom'))).toBe('Something went wrong. Please try again.');
  });

  it('translates validation rules and maps them onto form fields', () => {
    expect(fieldErrorMessage({ field: 'name', rule: 'min', param: '3' })).toBe('Must be at least 3 characters.');
    const setError = vi.fn();
    const err = new ApiError(422, 'VALIDATION_FAILED', '', { fields: [{ field: 'email', rule: 'email' }, { field: 'other', rule: 'required' }] });
    expect(applyFieldErrors(err, setError, ['email', 'password'])).toBe(false);
    expect(setError).toHaveBeenCalledWith('email', { type: 'server', message: 'Enter a valid email address.' });
    expect(applyFieldErrors(new Error('x'), setError, ['email'])).toBe(false);
  });
});
