import { useFormat } from '@/i18n/useFormat';

/** Formats the duration of a run, job or step; live while it's still going. */
export function useElapsed() {
  const fmt = useFormat();
  return (started?: string | null, finished?: string | null) => {
    if (!started) return '';
    const end = finished ? Date.parse(finished) : Date.now();
    return fmt.duration(end - Date.parse(started));
  };
}
