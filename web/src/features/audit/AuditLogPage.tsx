import { useInfiniteQuery } from '@tanstack/react-query';
import { Download, RefreshCw } from 'lucide-react';
import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';
import { useCurrentOrg } from '@/app/org';
import { OrgAction, usePermissions } from '@/app/permissions';
import { PageHeader } from '@/components/common/PageHeader';
import { EmptyState, ErrorState, LoadingState } from '@/components/common/States';
import { Button } from '@/components/ui/button';
import { Card } from '@/components/ui/card';
import { selectClass } from '@/features/org/constants';
import { errorMessage } from '@/lib/api/errors';
import { downloadFile } from '@/lib/api/fetcher';
import { getExportAuditLogUrl, getListAuditLogQueryKey, listAuditLog } from '@/lib/api/generated/audit/audit';
import { useListMembers } from '@/lib/api/generated/members/members';
import { useListProjects } from '@/lib/api/generated/projects/projects';
import { AuditEntryItem } from './AuditEntryItem';
import { CATEGORY_KEYS, PERIODS, auditParams, emptyAuditFilters, exportFileName, type AuditFilters, type Category, type Period } from './auditView';

const PAGE_SIZE = 50;

/** Organization → Audit log (audit.view): every entry as a sentence, with filters and CSV export. */
export function AuditLogPage() {
  const { t, i18n } = useTranslation(['audit', 'common']);
  const org = useCurrentOrg();
  const perms = usePermissions(org.id);
  const members = useListMembers(org.id, { limit: 200 }, { query: { staleTime: 60_000 } });
  const projects = useListProjects(org.id, { limit: 200 }, { query: { staleTime: 60_000 } });
  const [filters, setFilters] = useState<AuditFilters>(emptyAuditFilters);
  const [anchor, setAnchor] = useState(() => Date.now());
  const [exporting, setExporting] = useState(false);
  const params = useMemo(() => auditParams(filters, anchor, i18n.resolvedLanguage ?? 'en'), [filters, anchor, i18n.resolvedLanguage]);

  const entries = useInfiniteQuery({
    queryKey: getListAuditLogQueryKey(org.id, params),
    queryFn: ({ pageParam, signal }) => listAuditLog(org.id, { ...params, limit: PAGE_SIZE, ...(pageParam ? { cursor: pageParam } : {}) }, { signal }),
    initialPageParam: '',
    getNextPageParam: (last) => last.next_cursor ?? undefined,
  });

  const apply = (next: Partial<AuditFilters>) => {
    setFilters((f) => ({ ...f, ...next }));
    setAnchor(Date.now());
  };

  const exportCsv = async () => {
    setExporting(true);
    try {
      await downloadFile(getExportAuditLogUrl(org.id, params), exportFileName(org.slug, new Date()), 'text/csv');
    } catch (err) {
      toast.error(errorMessage(err));
    } finally {
      setExporting(false);
    }
  };

  const items = entries.data?.pages.flatMap((p) => p.items) ?? [];
  return (
    <div className="space-y-6">
      <PageHeader
        title={t('title')}
        description={t('description')}
        actions={
          perms.can(OrgAction.auditExport) ? (
            <Button variant="outline" disabled={exporting} title={t('export.hint')} onClick={() => void exportCsv()}>
              <Download aria-hidden />
              {t('export.button')}
            </Button>
          ) : undefined
        }
      />
      <div className="flex flex-wrap items-center gap-2">
        <label className="sr-only" htmlFor="audit-category">
          {t('filters.category')}
        </label>
        <select
          id="audit-category"
          className={`${selectClass} w-auto`}
          value={filters.category}
          onChange={(e) => {
            apply({ category: e.target.value as Category | '' });
          }}
        >
          <option value="">{t('filters.allCategories')}</option>
          {CATEGORY_KEYS.map((c) => (
            <option key={c} value={c}>
              {t(`categories.${c}`)}
            </option>
          ))}
        </select>
        <label className="sr-only" htmlFor="audit-actor">
          {t('filters.actor')}
        </label>
        <select
          id="audit-actor"
          className={`${selectClass} w-auto max-w-56`}
          value={filters.actor}
          onChange={(e) => {
            apply({ actor: e.target.value });
          }}
        >
          <option value="">{t('filters.allActors')}</option>
          {(members.data?.items ?? []).map((m) => (
            <option key={m.user_id} value={m.user_id}>
              {m.display_name}
            </option>
          ))}
        </select>
        <label className="sr-only" htmlFor="audit-project">
          {t('filters.project')}
        </label>
        <select
          id="audit-project"
          className={`${selectClass} w-auto max-w-56`}
          value={filters.project}
          onChange={(e) => {
            apply({ project: e.target.value });
          }}
        >
          <option value="">{t('filters.allProjects')}</option>
          {(projects.data?.items ?? []).map((p) => (
            <option key={p.id} value={p.id}>
              {p.name}
            </option>
          ))}
        </select>
        <label className="sr-only" htmlFor="audit-period">
          {t('filters.period')}
        </label>
        <select
          id="audit-period"
          className={`${selectClass} w-auto`}
          value={filters.period}
          onChange={(e) => {
            apply({ period: e.target.value as Period });
          }}
        >
          {PERIODS.map((p) => (
            <option key={p} value={p}>
              {t(`periods.${p}`)}
            </option>
          ))}
        </select>
        <Button
          variant="ghost"
          size="icon"
          aria-label={t('common:actions.retry')}
          onClick={() => {
            setAnchor(Date.now());
          }}
        >
          <RefreshCw aria-hidden />
        </Button>
      </div>
      {entries.isPending ? (
        <LoadingState />
      ) : entries.isError ? (
        <ErrorState error={entries.error} onRetry={() => void entries.refetch()} />
      ) : items.length === 0 ? (
        <EmptyState title={t('empty')} description={t('emptyHint')} />
      ) : (
        <div className="space-y-3">
          <Card className="gap-0 overflow-hidden p-0">
            <ul data-testid="audit-entries">
              {items.map((e) => (
                <AuditEntryItem key={e.id} entry={e} />
              ))}
            </ul>
          </Card>
          {entries.hasNextPage && (
            <div className="flex justify-center">
              <Button variant="outline" disabled={entries.isFetchingNextPage} onClick={() => void entries.fetchNextPage()}>
                {t('loadMore')}
              </Button>
            </div>
          )}
        </div>
      )}
    </div>
  );
}
