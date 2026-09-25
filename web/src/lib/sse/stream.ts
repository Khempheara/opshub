import i18n from '@/i18n';
import { getAccessToken, refreshSession } from '@/auth/session';
import { SSEParser, type SSEMessage } from './parser';

export interface StreamOptions {
  onMessage: (m: SSEMessage) => void;
  /** Called once the server sent everything (e.g. "end"); the stream then stops reconnecting. */
  isFinal?: (m: SSEMessage) => boolean;
  onError?: (status: number) => void;
}

/**
 * Subscribes to an SSE endpoint with the in-memory access token (EventSource can't send
 * headers). Reconnects with backoff and Last-Event-ID; refreshes the session on 401; stops on
 * other 4xx (reported through onError) or when isFinal matches. Returns a function that
 * closes the stream.
 */
export function openStream(url: string, opts: StreamOptions): () => void {
  const controller = new AbortController();
  let lastId: string | undefined;
  let attempt = 0;
  let done = false;
  // A function, not a property read, so TypeScript doesn't narrow it: abort() happens elsewhere.
  const aborted = () => controller.signal.aborted;

  const run = async () => {
    while (!done && !aborted()) {
      try {
        const headers: Record<string, string> = { Accept: 'text/event-stream', 'Accept-Language': i18n.resolvedLanguage ?? 'en' };
        const token = getAccessToken();
        if (token) headers.Authorization = `Bearer ${token}`;
        if (lastId !== undefined) headers['Last-Event-ID'] = lastId;
        const res = await fetch(url, { headers, signal: controller.signal, credentials: 'same-origin', cache: 'no-store' });
        if (res.status === 401 && (await refreshSession())) continue;
        if (!res.ok || !res.body) {
          if (res.status >= 400 && res.status < 500) {
            done = true;
            opts.onError?.(res.status);
            return;
          }
          throw new Error(`stream failed: ${String(res.status)}`);
        }
        attempt = 0;
        const reader = res.body.pipeThrough(new TextDecoderStream()).getReader();
        const parser = new SSEParser();
        for (;;) {
          const { value, done: eof } = await reader.read();
          if (eof) break;
          for (const m of parser.push(value)) {
            if (m.id !== undefined) lastId = m.id;
            opts.onMessage(m);
            if (opts.isFinal?.(m)) {
              done = true;
              controller.abort();
              return;
            }
          }
        }
      } catch {
        if (aborted()) return;
      }
      // Server closed (it ends streams every few minutes) or the network failed: reconnect.
      attempt += 1;
      const delay = Math.min(30_000, 500 * 2 ** Math.min(attempt, 6));
      await new Promise((r) => setTimeout(r, attempt === 1 ? 250 : delay));
    }
  };
  void run();
  return () => {
    done = true;
    controller.abort();
  };
}
