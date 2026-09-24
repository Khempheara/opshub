import i18n from 'i18next';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { errorMessage, fieldErrorMessage } from './errors';
import { ApiError, customFetch, NetworkError } from './fetcher';

function mockFetch(impl: () => Promise<Response>) {
  return vi.spyOn(globalThis, 'fetch').mockImplementation(impl);
}

afterEach(async () => {
  vi.restoreAllMocks();
  await i18n.changeLanguage('en');
});

describe('customFetch', () => {
  it('returns the JSON body and sends the active language', async () => {
    await i18n.changeLanguage('km');
    const spy = mockFetch(() => Promise.resolve(Response.json({ status: 'ok' })));
    await expect(customFetch('/healthz')).resolves.toEqual({ status: 'ok' });
    const headers = new Headers(spy.mock.calls[0]?.[1]?.headers);
    expect(headers.get('Accept-Language')).toBe('km');
  });

  it('maps the error envelope to ApiError', async () => {
    mockFetch(() =>
      Promise.resolve(
        Response.json(
          { error: { code: 'VALIDATION_FAILED', message: 'x', details: { fields: [{ field: 'email', rule: 'email' }] } } },
          { status: 422 },
        ),
      ),
    );
    const err = await customFetch('/api/v1/x').catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).code).toBe('VALIDATION_FAILED');
    expect((err as ApiError).fieldErrors).toEqual([{ field: 'email', rule: 'email' }]);
  });

  it('handles non-JSON error pages', async () => {
    mockFetch(() => Promise.resolve(new Response('<html>Bad Gateway</html>', { status: 502 })));
    const err = await customFetch('/api/v1/x').catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).code).toBe('INTERNAL');
  });

  it('wraps connection failures in NetworkError', async () => {
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
  });

  it('falls back for unknown codes and non-API errors', () => {
    expect(errorMessage(new ApiError(418, 'NEW_CODE_FROM_NEWER_SERVER', ''))).toBe(
      'Something went wrong. Please try again.',
    );
    expect(errorMessage(new Error('boom'))).toBe('Something went wrong. Please try again.');
  });

  it('translates validation rules with params', () => {
    expect(fieldErrorMessage({ field: 'name', rule: 'min', param: '3' })).toBe('Must be at least 3 characters.');
    expect(fieldErrorMessage({ field: 'x', rule: 'unknown_rule' })).toBe('This value is invalid.');
  });
});
