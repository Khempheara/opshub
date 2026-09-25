import { CSRF_COOKIE, CSRF_HEADER, clearSession, getAccessToken, readCookie, refreshSession } from '@/auth/session';
import i18n from '@/i18n';

export interface FieldError {
  field: string;
  rule: string;
  param?: string;
}

/** Error thrown for every non-2xx response, carrying the API's stable error code. */
export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly details: Record<string, unknown>;

  constructor(status: number, code: string, message: string, details: Record<string, unknown> = {}) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.code = code;
    this.details = details;
  }

  get fieldErrors(): FieldError[] {
    const fields = this.details.fields;
    return Array.isArray(fields) ? (fields as FieldError[]) : [];
  }

  /** Seconds until retry is allowed (ACCOUNT_LOCKED, RATE_LIMITED). */
  get retryAfterSeconds(): number | undefined {
    const v = this.details.retry_after_seconds;
    return typeof v === 'number' ? v : undefined;
  }
}

/** Thrown when the server could not be reached at all. */
export class NetworkError extends Error {
  constructor(cause: unknown) {
    super('network error', { cause });
    this.name = 'NetworkError';
  }
}

interface ErrorEnvelope {
  error?: { code?: string; message?: string; details?: Record<string, unknown> };
}

// Endpoints that manage the session themselves must not trigger the refresh-and-retry.
const NO_RETRY = /\/api\/v1\/auth\//;

async function send(url: string, init: RequestInit): Promise<Response> {
  const headers = new Headers(init.headers);
  if (!headers.has('Accept')) headers.set('Accept', 'application/json');
  headers.set('Accept-Language', i18n.resolvedLanguage ?? 'en');
  const token = getAccessToken();
  if (token && !headers.has('Authorization')) headers.set('Authorization', `Bearer ${token}`);
  const csrf = readCookie(CSRF_COOKIE);
  if (csrf) headers.set(CSRF_HEADER, csrf);
  try {
    return await fetch(url, { ...init, headers, credentials: 'same-origin' });
  } catch (err) {
    if (err instanceof DOMException && err.name === 'AbortError') throw err;
    throw new NetworkError(err);
  }
}

async function sendWithRefresh(url: string, init: RequestInit): Promise<Response> {
  let res = await send(url, init);
  if (res.status === 401 && !NO_RETRY.test(url) && getAccessToken() !== null) {
    if (await refreshSession()) {
      res = await send(url, init);
    } else {
      clearSession();
    }
  }
  return res;
}

/**
 * Downloads a file (logs, artifacts) with the same authentication as API calls and saves it
 * under fileName. Errors are ApiError/NetworkError, as for JSON calls.
 */
export async function downloadFile(url: string, fileName: string, accept = '*/*'): Promise<void> {
  const res = await sendWithRefresh(url, { headers: { Accept: accept } });
  if (!res.ok) {
    let e: ErrorEnvelope['error'];
    try {
      e = ((await res.json()) as ErrorEnvelope).error;
    } catch {
      e = undefined;
    }
    throw new ApiError(res.status, e?.code ?? 'INTERNAL', e?.message ?? res.statusText, e?.details ?? {});
  }
  const href = URL.createObjectURL(await res.blob());
  const a = document.createElement('a');
  a.href = href;
  a.download = fileName;
  a.click();
  URL.revokeObjectURL(href);
}

/**
 * Mutator used by every orval-generated function. Adds the access token, language and CSRF
 * header; on 401 it refreshes the session once and retries; errors become ApiError/NetworkError.
 */
export async function customFetch<T>(url: string, init: RequestInit = {}): Promise<T> {
  const res = await sendWithRefresh(url, init);

  const text = await res.text();
  let body: unknown;
  try {
    body = text ? JSON.parse(text) : undefined;
  } catch {
    // Non-JSON body (e.g. an HTML 502 page from a proxy).
    if (res.ok) throw new ApiError(res.status, 'INTERNAL', 'invalid JSON response');
  }

  if (!res.ok) {
    const e = (body as ErrorEnvelope | undefined)?.error;
    throw new ApiError(res.status, e?.code ?? 'INTERNAL', e?.message ?? res.statusText, e?.details ?? {});
  }
  return body as T;
}

export default customFetch;
