import { Download } from 'lucide-react';
import { useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';
import { downloadFile } from '@/lib/api/fetcher';
import { Button } from '@/components/ui/button';
import { Checkbox } from '@/components/ui/checkbox';
import { parseAnsi, type AnsiSegment } from '@/lib/ansi';
import { errorMessage } from '@/lib/api/errors';
import { getGetJobLogsUrl } from '@/lib/api/generated/pipelines/pipelines';
import { cn } from '@/lib/utils';
import type { LogLine } from './live';

/** Lines rendered at most; the download has everything. */
const MAX_LINES = 5000;

// 16 terminal colors tuned for the always-dark log panel.
const FG = [
  'text-zinc-500', 'text-red-400', 'text-green-400', 'text-yellow-300', 'text-blue-400', 'text-fuchsia-400', 'text-cyan-300', 'text-zinc-200',
  'text-zinc-400', 'text-red-300', 'text-green-300', 'text-yellow-200', 'text-blue-300', 'text-fuchsia-300', 'text-cyan-200', 'text-white',
];
const BG = [
  'bg-zinc-800', 'bg-red-900', 'bg-green-900', 'bg-yellow-900', 'bg-blue-900', 'bg-fuchsia-900', 'bg-cyan-900', 'bg-zinc-600',
  'bg-zinc-700', 'bg-red-800', 'bg-green-800', 'bg-yellow-800', 'bg-blue-800', 'bg-fuchsia-800', 'bg-cyan-800', 'bg-zinc-500',
];

function segClass(s: AnsiSegment): string | undefined {
  const c = cn(
    s.fg !== undefined && FG[s.fg],
    s.bg !== undefined && BG[s.bg],
    s.bold && 'font-bold',
    s.dim && 'opacity-70',
    s.italic && 'italic',
    s.underline && 'underline',
  );
  return c || undefined;
}

/** Terminal-style log with ANSI colors, line numbers and follow mode. */
export function LogViewer({ chunks, jobId, fileName, emptyText }: { chunks: LogLine[]; jobId: string; fileName: string; emptyText: string }) {
  const { t } = useTranslation('pipeline');
  const [follow, setFollow] = useState(true);
  const box = useRef<HTMLDivElement>(null);

  const { lines, hidden } = useMemo(() => {
    const all = chunks.map((c) => c.content).join('').split('\n');
    if (all.at(-1) === '') all.pop();
    const hidden = Math.max(0, all.length - MAX_LINES);
    return { lines: all.slice(hidden).map((l) => parseAnsi(l)), hidden };
  }, [chunks]);

  useEffect(() => {
    if (follow && box.current) box.current.scrollTop = box.current.scrollHeight;
  }, [lines, follow]);

  const download = async () => {
    try {
      await downloadFile(getGetJobLogsUrl(jobId), fileName, 'text/plain');
    } catch (err) {
      toast.error(errorMessage(err));
    }
  };

  return (
    <div className="space-y-2">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <label className="flex items-center gap-2 text-sm">
          <Checkbox
            checked={follow}
            onCheckedChange={(c) => {
              setFollow(c === true);
            }}
          />
          {t('job.follow')}
        </label>
        <Button variant="outline" size="sm" onClick={() => void download()} disabled={chunks.length === 0}>
          <Download aria-hidden />
          {t('job.download')}
        </Button>
      </div>
      {/* Logs are machine output: always left-to-right, monospace, dark. */}
      <div
        ref={box}
        dir="ltr"
        tabIndex={0}
        role="log"
        aria-label={t('job.logs')}
        aria-live="off"
        data-testid="job-log"
        className="focus-visible:ring-ring/50 max-h-[32rem] min-h-40 overflow-auto rounded-md bg-zinc-950 p-3 font-mono text-xs leading-5 text-zinc-200 outline-none focus-visible:ring-[3px]"
        onScroll={(e) => {
          const el = e.currentTarget;
          const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
          if (!atBottom && follow) setFollow(false);
        }}
      >
        {hidden > 0 && <p className="mb-2 text-zinc-500">{t('job.hiddenLines', { count: hidden })}</p>}
        {lines.length === 0 ? (
          <p className="text-zinc-500">{emptyText}</p>
        ) : (
          <table className="w-full border-collapse">
            <tbody>
              {lines.map((segs, i) => (
                <tr key={i + hidden}>
                  <td className="pe-4 text-end align-top text-zinc-600 select-none">{i + hidden + 1}</td>
                  <td className="break-all whitespace-pre-wrap">
                    {segs.map((s, j) => (
                      <span key={j} className={segClass(s)}>
                        {s.text}
                      </span>
                    ))}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}
