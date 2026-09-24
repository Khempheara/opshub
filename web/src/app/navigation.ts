/** Only same-site paths are allowed as post-login destinations (no open redirects). */
export function safeNext(next: string | null): string {
  if (!next?.startsWith('/') || next.startsWith('//') || next.startsWith('/\\')) return '/';
  return next;
}
