import { useQueryClient } from '@tanstack/react-query';
import { Pencil, Plus, Send, Trash2 } from 'lucide-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';
import { RequirePermission } from '@/app/guards';
import { useCurrentOrg } from '@/app/org';
import { OrgAction, usePermissions } from '@/app/permissions';
import { ConfirmDialog } from '@/components/common/ConfirmDialog';
import { PageHeader } from '@/components/common/PageHeader';
import { EmptyState, ErrorState, LoadingState } from '@/components/common/States';
import { Button } from '@/components/ui/button';
import { Card } from '@/components/ui/card';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table';
import { useFormat } from '@/i18n/useFormat';
import { errorMessage } from '@/lib/api/errors';
import type { NotificationChannel } from '@/lib/api/generated/model';
import {
  deleteNotificationChannel,
  getListNotificationChannelsQueryKey,
  testNotificationChannel,
  useListNotificationChannels,
} from '@/lib/api/generated/monitoring/monitoring';
import { ChannelDialog } from './ChannelDialog';
import { MonitoringTabs } from './MonitoringTabs';

/** Each language by its own name. */
const LANGUAGE_NAMES: Record<string, string> = { en: 'English', km: 'ខ្មែរ' };

function Destination({ c }: { c: NotificationChannel }) {
  switch (c.kind) {
    case 'telegram':
      return <>{c.config.chat_id}</>;
    case 'email':
      return <>{(c.config.addresses ?? []).join(', ')}</>;
    case 'webhook':
      return <>{c.config.url}</>;
    default:
      return <>—</>;
  }
}

function Channels() {
  const { t } = useTranslation(['monitoring', 'common']);
  const org = useCurrentOrg();
  const fmt = useFormat();
  const perms = usePermissions(org.id);
  const canManage = perms.can(OrgAction.channelManage);
  const queryClient = useQueryClient();
  const [open, setOpen] = useState<{ channel?: NotificationChannel } | null>(null);
  const [testing, setTesting] = useState<string | null>(null);
  const channels = useListNotificationChannels(org.id);
  const refresh = () => queryClient.invalidateQueries({ queryKey: getListNotificationChannelsQueryKey(org.id) });

  const test = async (c: NotificationChannel) => {
    setTesting(c.id);
    try {
      const res = await testNotificationChannel(c.id);
      if (res.ok) toast.success(t('channels.testSent'));
      else toast.error(t('channels.testError', { error: res.error ?? '' }));
      await refresh();
    } catch (err) {
      toast.error(errorMessage(err));
    } finally {
      setTesting(null);
    }
  };
  const remove = async (c: NotificationChannel) => {
    try {
      await deleteNotificationChannel(c.id);
      await refresh();
      toast.success(t('channels.deleted'));
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
              {t('channels.add')}
            </Button>
          ) : undefined
        }
      />
      <MonitoringTabs />
      {channels.isPending ? (
        <LoadingState />
      ) : channels.isError ? (
        <ErrorState error={channels.error} onRetry={() => void channels.refetch()} />
      ) : channels.data.items.length === 0 ? (
        <EmptyState title={t('channels.empty')} description={canManage ? t('channels.emptyHint') : undefined} />
      ) : (
        <Card className="gap-0 overflow-x-auto p-0">
          <Table data-testid="channels-table">
            <TableHeader>
              <TableRow>
                <TableHead>{t('channels.columns.name')}</TableHead>
                <TableHead>{t('channels.columns.target')}</TableHead>
                <TableHead>{t('channels.columns.language')}</TableHead>
                <TableHead>{t('channels.columns.test')}</TableHead>
                {canManage && (
                  <TableHead>
                    <span className="sr-only">{t('channels.edit')}</span>
                  </TableHead>
                )}
              </TableRow>
            </TableHeader>
            <TableBody>
              {channels.data.items.map((c) => (
                <TableRow key={c.id} data-testid="channel-row">
                  <TableCell>
                    <span className="font-medium break-words">{c.name}</span>
                    <span className="text-muted-foreground block text-xs">{t(`channelKinds.${c.kind}`)}</span>
                  </TableCell>
                  <TableCell className="text-muted-foreground max-w-64 font-mono text-xs break-all whitespace-normal" dir="ltr">
                    <Destination c={c} />
                  </TableCell>
                  <TableCell className="text-sm">{c.locale ? LANGUAGE_NAMES[c.locale] : t('channels.recipientLanguage')}</TableCell>
                  <TableCell className="text-xs whitespace-nowrap">
                    {c.last_test_at === null ? (
                      <span className="text-muted-foreground">{t('channels.testNever')}</span>
                    ) : c.last_test_ok ? (
                      <span className="text-success">{t('channels.testOk', { time: fmt.relative(c.last_test_at) })}</span>
                    ) : (
                      <span className="text-destructive">{t('channels.testFailed', { time: fmt.relative(c.last_test_at) })}</span>
                    )}
                  </TableCell>
                  {canManage && (
                    <TableCell>
                      <div className="flex justify-end gap-1">
                        <Button variant="outline" size="sm" disabled={testing === c.id} onClick={() => void test(c)}>
                          <Send aria-hidden />
                          {t('channels.test')}
                        </Button>
                        <Button
                          variant="ghost"
                          size="icon"
                          aria-label={t('channels.editTitle', { name: c.name })}
                          onClick={() => {
                            setOpen({ channel: c });
                          }}
                        >
                          <Pencil aria-hidden />
                        </Button>
                        <ConfirmDialog
                          trigger={
                            <Button variant="ghost" size="icon" aria-label={t('channels.deleteTitle', { name: c.name })}>
                              <Trash2 aria-hidden />
                            </Button>
                          }
                          title={t('channels.deleteTitle', { name: c.name })}
                          description={t('channels.deleteBody')}
                          confirmLabel={t('common:actions.delete')}
                          onConfirm={() => remove(c)}
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
        <ChannelDialog
          orgId={org.id}
          channel={open.channel}
          onClose={() => {
            setOpen(null);
          }}
        />
      )}
    </div>
  );
}

/** /o/:orgSlug/monitoring/channels — Developers and up (channel.view). */
export function ChannelsPage() {
  return (
    <RequirePermission action={OrgAction.channelView}>
      <Channels />
    </RequirePermission>
  );
}
