import { useQueryClient } from '@tanstack/react-query';
import { Pencil, Plus, Trash2 } from 'lucide-react';
import { useState } from 'react';
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
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table';
import { useListAssets } from '@/lib/api/generated/infrastructure/infrastructure';
import type { AlertRule } from '@/lib/api/generated/model';
import { deleteAlertRule, getListAlertRulesQueryKey, useListAlertRules, useListMonitors } from '@/lib/api/generated/monitoring/monitoring';
import { errorMessage } from '@/lib/api/errors';
import { watchesMonitors } from './forms';
import { MonitoringTabs } from './MonitoringTabs';
import { RuleDialog } from './RuleDialog';
import { SeverityBadge } from './status';

/** What a rule watches, in words. */
function Watches({ rule, names }: { rule: AlertRule; names: Map<string, string> }) {
  const { t } = useTranslation('monitoring');
  const all = watchesMonitors(rule.kind) ? t('rules.allMonitors') : rule.kind === 'certificate' ? t('rules.allDomains') : t('rules.allServers');
  const target = rule.target_id ? (names.get(rule.target_id) ?? rule.target_id) : all;
  return (
    <>
      <span className="block">{t(`ruleKinds.${rule.kind}`)}</span>
      <span className="text-muted-foreground block text-xs">
        {target}
        {rule.label && <> · {t('rules.withLabel', { label: rule.label })}</>}
      </span>
    </>
  );
}

/** Organization → Monitoring → Alert rules. */
export function RulesPage() {
  const { t } = useTranslation(['monitoring', 'common']);
  const org = useCurrentOrg();
  const perms = usePermissions(org.id);
  const canManage = perms.can(OrgAction.monitorManage);
  const queryClient = useQueryClient();
  const [open, setOpen] = useState<{ rule?: AlertRule } | null>(null);
  const rules = useListAlertRules(org.id, { query: { refetchInterval: 30_000 } });
  const monitors = useListMonitors(org.id);
  const assets = useListAssets(org.id);
  const names = new Map<string, string>([
    ...(monitors.data?.items ?? []).map((m): [string, string] => [m.id, m.name]),
    ...(assets.data?.items ?? []).map((a): [string, string] => [a.id, a.name]),
  ]);

  const remove = async (r: AlertRule) => {
    try {
      await deleteAlertRule(r.id);
      await queryClient.invalidateQueries({ queryKey: getListAlertRulesQueryKey(org.id) });
      toast.success(t('rules.deleted'));
    } catch (err) {
      toast.error(errorMessage(err));
    }
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
                setOpen({});
              }}
            >
              <Plus aria-hidden />
              {t('rules.add')}
            </Button>
          ) : undefined
        }
      />
      <MonitoringTabs />
      {rules.isPending ? (
        <LoadingState />
      ) : rules.isError ? (
        <ErrorState error={rules.error} onRetry={() => void rules.refetch()} />
      ) : rules.data.items.length === 0 ? (
        <EmptyState title={t('rules.empty')} description={canManage ? t('rules.emptyHint') : undefined} />
      ) : (
        <Card className="gap-0 overflow-x-auto p-0">
          <Table data-testid="rules-table">
            <TableHeader>
              <TableRow>
                <TableHead>{t('rules.columns.name')}</TableHead>
                <TableHead>{t('rules.columns.watches')}</TableHead>
                <TableHead>{t('rules.columns.severity')}</TableHead>
                <TableHead>{t('rules.columns.firing')}</TableHead>
                <TableHead>{t('rules.columns.notify')}</TableHead>
                {canManage && (
                  <TableHead>
                    <span className="sr-only">{t('rules.edit')}</span>
                  </TableHead>
                )}
              </TableRow>
            </TableHeader>
            <TableBody>
              {rules.data.items.map((r) => (
                <TableRow key={r.id} data-testid="rule-row">
                  <TableCell className="max-w-64 whitespace-normal">
                    <span className="font-medium break-words">{r.name}</span>
                    {!r.enabled && (
                      <Badge variant="secondary" className="ms-2">
                        {t('rules.disabled')}
                      </Badge>
                    )}
                  </TableCell>
                  <TableCell className="max-w-72 text-sm whitespace-normal">
                    <Watches rule={r} names={names} />
                  </TableCell>
                  <TableCell>
                    <SeverityBadge severity={r.severity} />
                  </TableCell>
                  <TableCell className={r.firing > 0 ? 'text-destructive font-medium tabular-nums' : 'tabular-nums'}>{r.firing}</TableCell>
                  <TableCell className="text-sm whitespace-nowrap">
                    {r.escalation.length === 0 ? t('rules.notifyNone') : t('rules.notifySteps', { count: r.escalation.length })}
                  </TableCell>
                  {canManage && (
                    <TableCell>
                      <div className="flex justify-end gap-1">
                        <Button
                          variant="ghost"
                          size="icon"
                          aria-label={t('rules.editTitle', { name: r.name })}
                          onClick={() => {
                            setOpen({ rule: r });
                          }}
                        >
                          <Pencil aria-hidden />
                        </Button>
                        <ConfirmDialog
                          trigger={
                            <Button variant="ghost" size="icon" aria-label={t('rules.deleteTitle', { name: r.name })}>
                              <Trash2 aria-hidden />
                            </Button>
                          }
                          title={t('rules.deleteTitle', { name: r.name })}
                          description={t('rules.deleteBody')}
                          confirmLabel={t('common:actions.delete')}
                          onConfirm={() => remove(r)}
                        />
                      </div>
                    </TableCell>
                  )}
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Card>
      )}
      {open && (
        <RuleDialog
          orgId={org.id}
          rule={open.rule}
          onClose={() => {
            setOpen(null);
          }}
        />
      )}
    </div>
  );
}
