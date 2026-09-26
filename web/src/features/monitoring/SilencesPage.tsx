import { useQueryClient } from '@tanstack/react-query';
import { Plus } from 'lucide-react';
import { useId, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';
import { useCurrentOrg } from '@/app/org';
import { OrgAction, usePermissions } from '@/app/permissions';
import { ConfirmDialog } from '@/components/common/ConfirmDialog';
import { PageHeader } from '@/components/common/PageHeader';
import { EmptyState, ErrorState, LoadingState } from '@/components/common/States';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card } from '@/components/ui/card';
import { Checkbox } from '@/components/ui/checkbox';
import { Label } from '@/components/ui/label';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table';
import { useFormat } from '@/i18n/useFormat';
import { errorMessage } from '@/lib/api/errors';
import type { Silence } from '@/lib/api/generated/model';
import { expireSilence, getListAlertsQueryKey, getListSilencesQueryKey, useListAlertRules, useListSilences } from '@/lib/api/generated/monitoring/monitoring';
import { MonitoringTabs } from './MonitoringTabs';
import { SilenceDialog } from './SilenceDialog';

function Matchers({ s }: { s: Silence }) {
  const { t } = useTranslation('monitoring');
  const parts: string[] = [];
  if (s.rule_id) parts.push(t('silences.matcherRule', { name: s.rule_name ?? '—' }));
  if (s.subject_id) parts.push(t('silences.matcherSubject'));
  if (s.label) parts.push(t('silences.matcherLabel', { label: s.label }));
  if (s.severity) parts.push(t('silences.matcherSeverity', { severity: t(`severity.${s.severity}`) }));
  return <>{parts.join(' · ')}</>;
}

/** Organization → Monitoring → Silences. */
export function SilencesPage() {
  const { t } = useTranslation(['monitoring', 'common']);
  const org = useCurrentOrg();
  const fmt = useFormat();
  const perms = usePermissions(org.id);
  const canManage = perms.can(OrgAction.monitorManage);
  const queryClient = useQueryClient();
  const expiredId = useId();
  const [showExpired, setShowExpired] = useState(false);
  const [adding, setAdding] = useState(false);
  const silences = useListSilences(org.id, { include_expired: showExpired });
  const rules = useListAlertRules(org.id, { query: { enabled: adding } });

  const expire = async (s: Silence) => {
    try {
      await expireSilence(s.id);
      await queryClient.invalidateQueries({ queryKey: getListSilencesQueryKey(org.id) });
      await queryClient.invalidateQueries({ queryKey: getListAlertsQueryKey(org.id) });
      toast.success(t('silences.expiredToast'));
    } catch (err) {
      toast.error(errorMessage(err));
    }
  };

  // Silences are listed for this moment; the list refetches after changes.
  const [now] = useState(() => Date.now());
  const statusOf = (s: Silence): 'active' | 'upcoming' | 'expired' => {
    if (s.active) return 'active';
    return Date.parse(s.starts_at) > now ? 'upcoming' : 'expired';
  };

  return (
    <div className="space-y-6">
      <PageHeader
        title={t('title')}
        description={t('description')}
        actions={
          canManage ? (
            <Button
              onClick={() => {
                setAdding(true);
              }}
            >
              <Plus aria-hidden />
              {t('silences.add')}
            </Button>
          ) : undefined
        }
      />
      <MonitoringTabs />
      <div className="flex items-center gap-2">
        <Checkbox
          id={expiredId}
          checked={showExpired}
          onCheckedChange={(c) => {
            setShowExpired(c === true);
          }}
        />
        <Label htmlFor={expiredId} className="font-normal">
          {t('silences.showExpired')}
        </Label>
      </div>
      {silences.isPending ? (
        <LoadingState />
      ) : silences.isError ? (
        <ErrorState error={silences.error} onRetry={() => void silences.refetch()} />
      ) : silences.data.items.length === 0 ? (
        <EmptyState title={t('silences.empty')} description={t('silences.emptyHint')} />
      ) : (
        <Card className="gap-0 overflow-x-auto p-0">
          <Table data-testid="silences-table">
            <TableHeader>
              <TableRow>
                <TableHead>{t('silences.columns.matchers')}</TableHead>
                <TableHead>{t('silences.columns.window')}</TableHead>
                <TableHead>{t('silences.columns.comment')}</TableHead>
                <TableHead>{t('silences.columns.by')}</TableHead>
                {canManage && (
                  <TableHead>
                    <span className="sr-only">{t('silences.expire')}</span>
                  </TableHead>
                )}
              </TableRow>
            </TableHeader>
            <TableBody>
              {silences.data.items.map((s) => {
                const status = statusOf(s);
                return (
                  <TableRow key={s.id} data-testid="silence-row">
                    <TableCell className="max-w-72 text-sm whitespace-normal">
                      <Matchers s={s} />
                    </TableCell>
                    <TableCell className="text-xs">
                      <Badge variant={status === 'active' ? 'default' : 'secondary'}>{t(`silences.${status}`)}</Badge>
                      <span className="text-muted-foreground mt-1 block whitespace-nowrap">
                        {t('silences.from', {
                          from: fmt.dateTime(s.starts_at, { dateStyle: 'short', timeStyle: 'short' }),
                          to: fmt.dateTime(s.ends_at, { dateStyle: 'short', timeStyle: 'short' }),
                        })}
                      </span>
                    </TableCell>
                    <TableCell className="max-w-64 text-sm break-words whitespace-normal">{s.comment || '—'}</TableCell>
                    <TableCell className="text-sm">{s.created_by_name ?? '—'}</TableCell>
                    {canManage && (
                      <TableCell>
                        {status !== 'expired' && (
                          <ConfirmDialog
                            trigger={
                              <Button variant="outline" size="sm">
                                {t('silences.expire')}
                              </Button>
                            }
                            title={t('silences.expireTitle')}
                            description={t('silences.expireBody')}
                            confirmLabel={t('silences.expire')}
                            onConfirm={() => expire(s)}
                          />
                        )}
                      </TableCell>
                    )}
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        </Card>
      )}
      {adding && (
        <SilenceDialog
          orgId={org.id}
          rules={rules.data?.items ?? []}
          onClose={() => {
            setAdding(false);
          }}
        />
      )}
    </div>
  );
}
