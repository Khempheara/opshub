/**
 * A fresh Idempotency-Key for one user action. Create it when the form opens (not on every
 * submit) so a double click or a network retry replays the first response instead of
 * creating a duplicate.
 */
export function newIdempotencyKey(): string {
  return crypto.randomUUID();
}

export function idempotencyHeaders(key: string): { headers: Record<string, string> } {
  return { headers: { 'Idempotency-Key': key } };
}

export function ifMatch(version: number): { headers: Record<string, string> } {
  return { headers: { 'If-Match': `"v${String(version)}"` } };
}
