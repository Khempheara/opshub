import { ChevronDown, ChevronRight, Radio, RefreshCw, Search } from 'lucide-react';
import { useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';
import { useCurrentOrg } from '@/app/org';
import { PageHeader } from '@/components/common/PageHeader';
import { EmptyState, ErrorState, LoadingState } from '@/components/common/States';
import { Button } from '@/components/ui/button';
import { Card } from '@/components/ui/card';
import { Input } from '@/components/ui/input';
import { selectClass } from '@/features/org/constants';
import { useFormat } from '@/i18n/useFormat';
import { errorMessage } from '@/lib/api/errors';
import { searchLogs, useListLogServices, useSearchLogs } from '@/lib/api/generated/logs/logs';
import type { LogEntry, LogLevel, LogSource, SearchLogsParams } from '@/lib/api/generated/model';
import { cn } from '@/lib/utils';
import { LogsTabs } from './LogsTabs';
import { LEVELS, RANGES, SOURCES, appendOlder, attributeEntries, emptyFilters, levelClass, mergeNewer, searchParams, type Filters, type Range } from './logView';

const FOLLOW_INTERVAL_MS = 3000;
const TIME_FORMAT: Intl.DateTimeFormatOptions = {
  month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', second: '2-digit', fractionalSecondDigits: 3, hour12: false,
};

function LogLine({ entry }: { entry: LogEntry }) {
  const { t } = useTranslation('logs');
  const fmt = useFormat();
  const [open, setOpen] = useState(false);
  const attrs = attributeEntries(entry.attributes);
  return (
    <li className="border-b last:border-b-0" data-testid="log-line" data-level={entry.level}>
      <button
        type="button"
        className="hover:bg-accent/50 focus-visible:ring-ring/50 flex w-full min-w-0 flex-wrap items-start gap-x-2 gap-y-0.5 px-3 sm:flex-nowrap py-1.5 text-start outline-none focus-visible:ring-[3px]"
        aria-expanded={open}
        onClick={() => {
          setOpen((o) => !o);
        }}
      >
        {open ? <ChevronDown aria-hidden className="mt-0.5 size-3.5 shrink-0" /> : <ChevronRight aria-hidden className="mt-0.5 size-3.5 shrink-0" />}
        <time dateTime={entry.ts} className="text-muted-foreground shrink-0 font-mono text-xs leading-5 tabular-nums">
          {fmt.dateTime(entry.ts, TIME_FORMAT)}
        </time>
        <span className={cn('w-14 shrink-0 rounded border text-center font-mono text-[11px] leading-[18px] uppercase', levelClass[entry.level])}>{entry.level}</span>
        <span className="text-muted-foreground hidden max-w-48 shrink-0 truncate font-mono text-xs leading-5 sm:inline" dir="ltr" title={entry.service}>
          {entry.service}
        </span>
        {/* On phones the message gets its own line under the time and level, indented past the chevron. */}
        <span className="min-w-0 flex-1 basis-full ps-5.5 font-mono text-xs leading-5 break-words whitespace-pre-wrap sm:basis-0 sm:ps-0">
          {entry.message}
        </span>
      </button>
      {open && (
        <div className="bg-muted/40 space-y-2 px-3 py-2 ps-9 text-xs">
          <p className="font-mono sm:hidden" dir="ltr">
            {entry.service}
          </p>
          <p className="text-muted-foreground">
            {t(`sources.${entry.source}`)} · <span className="font-mono">{entry.ts}</span>
          </p>
          {attrs.length === 0 ? (
            <p className="text-muted-foreground">{t('search.noAttributes')}</p>
          ) : (
            <dl className="grid grid-cols-[minmax(0,max-content)_minmax(0,1fr)] gap-x-4 gap-y-1 font-mono" aria-label={t('search.attributes')}>
              {attrs.map(([k, v]) => (
                <div key={k} className="contents">
                  <dt className="text-muted-foreground break-all">{k}</dt>
                  <dd className="break-all whitespace-pre-wrap">{v}</dd>
                </div>
              ))}
            </dl>
          )}
        </div>
      )}
    </li>
  );
}

/**
 * The lines for one search. Remounted (keyed by the parameters) whenever the search changes,
 * so older pages and followed lines always belong to the current search.
 */
function LogResults({ orgId, params, follow }: { orgId: string; params: SearchLogsParams; follow: boolean }) {
  const { t } = useTranslation(['logs', 'common']);
  const base = useSearchLogs(orgId, params, { query: { refetchOnWindowFocus: false, staleTime: Infinity } });
  const [fresh, setFresh] = useState<LogEntry[]>([]);
  const [older, setOlder] = useState<LogEntry[]>([]);
  const [olderCursor, setOlderCursor] = useState<string | null | undefined>(undefined);
  const [trimmed, setTrimmed] = useState(false);
  const [loadingOlder, setLoadingOlder] = useState(false);
  const newest = useRef<string | null>(null);

  const baseData = base.data;
  useEffect(() => {
    if (!follow || !baseData) return;
    newest.current ??= baseData.newest_cursor;
    let busy = false;
    const id = window.setInterval(() => {
      if (busy) return;
      busy = true;
      searchLogs(orgId, { ...params, ...(newest.current ? { after: newest.current } : {}) })
        .then((r) => {
          newest.current = r.newest_cursor ?? newest.current;
          if (r.items.length === 0) return;
          setFresh((cur) => {
            const merged = mergeNewer(cur, r.items);
            if (merged.trimmed) setTrimmed(true);
            return merged.items;
          });
        })
        .catch((err: unknown) => {
          toast.error(errorMessage(err));
        })
        .finally(() => {
          busy = false;
        });
    }, FOLLOW_INTERVAL_MS);
    return () => {
      window.clearInterval(id);
    };
  }, [follow, baseData, orgId, params]);

  if (base.isPending) return <LoadingState />;
  if (base.isError) return <ErrorState error={base.error} onRetry={() => void base.refetch()} />;

  const items = appendOlder(mergeNewer(base.data.items, fresh, Number.MAX_SAFE_INTEGER).items, older);
  const nextCursor = trimmed ? null : olderCursor === undefined ? base.data.next_cursor : olderCursor;

  const loadOlder = async () => {
    if (!nextCursor) return;
    setLoadingOlder(true);
    try {
      const r = await searchLogs(orgId, { ...params, before: nextCursor });
      setOlder((cur) => appendOlder(cur, r.items));
      setOlderCursor(r.next_cursor);
    } catch (err) {
      toast.error(errorMessage(err));
    } finally {
      setLoadingOlder(false);
    }
  };

  if (items.length === 0) {
    return (
      <div className="space-y-2">
        {follow && <FollowingNote />}
        <EmptyState title={t('search.empty')} description={t('search.emptyHint')} />
      </div>
    );
  }
  return (
    <div className="space-y-3">
      <div className="text-muted-foreground flex flex-wrap items-center justify-between gap-2 text-sm">
        <span data-testid="log-count">{t('search.count', { count: items.length })}</span>
        {follow && <FollowingNote />}
      </div>
      <Card className="gap-0 overflow-hidden p-0">
        <ul data-testid="log-lines">
          {items.map((e) => (
            <LogLine key={e.id} entry={e} />
          ))}
        </ul>
      </Card>
      {trimmed && <p className="text-muted-foreground text-sm">{t('search.trimmed')}</p>}
      {nextCursor && (
        <div className="flex justify-center">
          <Button variant="outline" disabled={loadingOlder} onClick={() => void loadOlder()}>
            {t('search.loadOlder')}
          </Button>
        </div>
      )}
    </div>
  );
}

function FollowingNote() {
  const { t } = useTranslation('logs');
  return (
    <span className="text-success inline-flex items-center gap-1.5 text-sm" role="status">
      <span aria-hidden className="bg-success size-2 animate-pulse rounded-full" />
      {t('search.following')}
    </span>
  );
}

/** Organization → Logs: search every line the caller may see. */
export function LogsPage() {
  const { t } = useTranslation(['logs', 'common']);
  const org = useCurrentOrg();
  const services = useListLogServices(org.id, { query: { staleTime: 60_000 } });
  const [draft, setDraft] = useState('');
  const [filters, setFilters] = useState<Filters>(emptyFilters);
  const [anchor, setAnchor] = useState(() => Date.now());
  const [follow, setFollow] = useState(false);
  // Stable between renders (typing in the search box must not restart following).
  const params = useMemo(() => searchParams(filters, anchor), [filters, anchor]);

  const apply = (next: Partial<Filters>) => {
    setFilters((f) => ({ ...f, ...next }));
    setAnchor(Date.now());
  };

  return (
    <div className="space-y-6">
      <PageHeader title={t('title')} description={t('description')} />
      <LogsTabs />
      <form
        className="flex flex-wrap items-center gap-2"
        role="search"
        onSubmit={(e) => {
          e.preventDefault();
          apply({ q: draft });
        }}
      >
        <label className="sr-only" htmlFor="log-search">
          {t('search.label')}
        </label>
        <div className="flex min-w-0 flex-[1_1_18rem] gap-2">
          <Input
            id="log-search"
            className="min-w-0 flex-1"
            placeholder={t('search.placeholder')}
            value={draft}
            maxLength={200}
            onChange={(e) => {
              setDraft(e.target.value);
            }}
          />
          <Button type="submit" variant="secondary">
            <Search aria-hidden />
            <span className="sr-only sm:not-sr-only">{t('search.submit')}</span>
          </Button>
        </div>
        <label className="sr-only" htmlFor="log-level">
          {t('search.level')}
        </label>
        <select
          id="log-level"
          className={`${selectClass} w-auto`}
          value={filters.level}
          onChange={(e) => {
            apply({ level: e.target.value as LogLevel | '' });
          }}
        >
          <option value="">{t('search.allLevels')}</option>
          {LEVELS.map((l) => (
            <option key={l} value={l}>
              {t(`levels.${l}`)}
            </option>
          ))}
        </select>
        <label className="sr-only" htmlFor="log-source">
          {t('search.source')}
        </label>
        <select
          id="log-source"
          className={`${selectClass} w-auto`}
          value={filters.source}
          onChange={(e) => {
            apply({ source: e.target.value as LogSource | '' });
          }}
        >
          <option value="">{t('search.allSources')}</option>
          {SOURCES.map((s) => (
            <option key={s} value={s}>
              {t(`sources.${s}`)}
            </option>
          ))}
        </select>
        <label className="sr-only" htmlFor="log-service">
          {t('search.service')}
        </label>
        <select
          id="log-service"
          className={`${selectClass} w-auto max-w-56`}
          value={filters.service}
          onChange={(e) => {
            apply({ service: e.target.value });
          }}
        >
          <option value="">{t('search.allServices')}</option>
          {(services.data?.items ?? []).map((s) => (
            <option key={s} value={s}>
              {s}
            </option>
          ))}
        </select>
        <label className="sr-only" htmlFor="log-range">
          {t('search.range')}
        </label>
        <select
          id="log-range"
          className={`${selectClass} w-auto`}
          value={filters.range}
          onChange={(e) => {
            apply({ range: e.target.value as Range });
          }}
        >
          {RANGES.map((r) => (
            <option key={r} value={r}>
              {t(`ranges.${r}`)}
            </option>
          ))}
        </select>
        <Button
          type="button"
          variant={follow ? 'default' : 'outline'}
          aria-pressed={follow}
          onClick={() => {
            setFollow((f) => !f);
          }}
        >
          <Radio aria-hidden />
          {t('search.follow')}
        </Button>
        <Button
          type="button"
          variant="ghost"
          size="icon"
          aria-label={t('search.refresh')}
          onClick={() => {
            setAnchor(Date.now());
          }}
        >
          <RefreshCw aria-hidden />
        </Button>
      </form>
      <LogResults key={JSON.stringify(params)} orgId={org.id} params={params} follow={follow} />
    </div>
  );
}
