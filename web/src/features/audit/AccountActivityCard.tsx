import { useInfiniteQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { EmptyState, ErrorState, LoadingState } from '@/components/common/States';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { getListAccountActivityQueryKey, listAccountActivity } from '@/lib/api/generated/account/account';
import { AuditEntryItem } from './AuditEntryItem';

const PAGE_SIZE = 20;

/** Settings → Security: the signed-in person's own account events. */
export function AccountActivityCard() {
  const { t, i18n } = useTranslation('audit');
  const locale = i18n.resolvedLanguage === 'km' ? 'km' : 'en';
  const activity = useInfiniteQuery({
    queryKey: getListAccountActivityQueryKey({ locale }),
    queryFn: ({ pageParam, signal }) => listAccountActivity({ locale, limit: PAGE_SIZE, ...(pageParam ? { cursor: pageParam } : {}) }, { signal }),
    initialPageParam: '',
    getNextPageParam: (last) => last.next_cursor ?? undefined,
  });
  const items = activity.data?.pages.flatMap((p) => p.items) ?? [];

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t('activity.title')}</CardTitle>
        <CardDescription>{t('activity.description')}</CardDescription>
      </CardHeader>
      <CardContent className="space-y-3">
        {activity.isPending ? (
          <LoadingState rows={3} />
        ) : activity.isError ? (
          <ErrorState error={activity.error} onRetry={() => void activity.refetch()} />
        ) : items.length === 0 ? (
          <EmptyState title={t('activity.empty')} />
        ) : (
          <>
            <ul className="-mx-6 border-y" data-testid="account-activity">
              {items.map((e) => (
                <AuditEntryItem key={e.id} entry={e} compact />
              ))}
            </ul>
            {activity.hasNextPage && (
              <Button variant="outline" size="sm" disabled={activity.isFetchingNextPage} onClick={() => void activity.fetchNextPage()}>
                {t('activity.loadMore')}
              </Button>
            )}
          </>
        )}
      </CardContent>
    </Card>
  );
}
