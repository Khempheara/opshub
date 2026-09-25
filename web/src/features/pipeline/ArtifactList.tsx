import { Download, Package } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';
import { Button } from '@/components/ui/button';
import { useBytes } from '@/i18n/useBytes';
import { useFormat } from '@/i18n/useFormat';
import { errorMessage } from '@/lib/api/errors';
import { downloadFile } from '@/lib/api/fetcher';
import { getDownloadArtifactUrl, useListJobArtifacts } from '@/lib/api/generated/runners/runners';

/** A finished job's artifact archives with download buttons; nothing when there are none. */
export function ArtifactList({ jobId, finished }: { jobId: string; finished: boolean }) {
  const { t } = useTranslation('pipeline');
  const fmt = useFormat();
  const bytes = useBytes();
  const artifacts = useListJobArtifacts(jobId, { query: { enabled: finished } });
  const items = artifacts.data?.items ?? [];
  if (!finished || items.length === 0) return null;

  const download = async (id: string, name: string) => {
    try {
      await downloadFile(getDownloadArtifactUrl(id), name, 'application/gzip');
    } catch (err) {
      toast.error(errorMessage(err));
    }
  };

  return (
    <div className="space-y-2" data-testid="artifacts">
      <p className="text-sm font-medium">{t('artifacts.title')}</p>
      <ul className="divide-y rounded-md border">
        {items.map((a) => (
          <li key={a.id} className="flex flex-wrap items-center gap-3 px-3 py-2 text-sm">
            <Package aria-hidden className="text-muted-foreground size-4 shrink-0" />
            <span className="min-w-0 flex-1">
              <span className="font-mono break-all">{a.name}</span>
              <span className="text-muted-foreground block text-xs">
                {bytes(a.size_bytes)} · {t('artifacts.expires', { date: fmt.dateTime(a.expires_at, { dateStyle: 'medium' }) })}
              </span>
            </span>
            <Button variant="outline" size="sm" onClick={() => void download(a.id, a.name)}>
              <Download aria-hidden />
              {t('artifacts.download')}
            </Button>
          </li>
        ))}
      </ul>
    </div>
  );
}
