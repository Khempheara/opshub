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

/**
 * Mutator used by every orval-generated hook. Sends cookies (same-origin session),
 * the active language, and normalizes errors into ApiError / NetworkError.
 */
export async function customFetch<T>(url: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers);
  headers.set('Accept', 'application/json');
  headers.set('Accept-Language', i18n.resolvedLanguage ?? 'en');

  let res: Response;
  try {
    res = await fetch(url, { ...init, headers, credentials: 'same-origin' });
  } catch (err) {
    if (err instanceof DOMException && err.name === 'AbortError') throw err;
    throw new NetworkError(err);
  }

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
