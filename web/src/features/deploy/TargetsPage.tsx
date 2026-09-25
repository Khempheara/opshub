import { useQueryClient } from '@tanstack/react-query';
import { CheckCircle2, CircleHelp, Pencil, Plug, Plus, ShieldCheck, Trash2, XCircle } from 'lucide-react';
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
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table';
import { useFormat } from '@/i18n/useFormat';
import { errorMessage } from '@/lib/api/errors';
import type { DeployTarget, TargetTestResult } from '@/lib/api/generated/model';
import {
  deleteDeployTarget,
  getListDeployTargetsQueryKey,
  testDeployTarget,
  updateDeployTarget,
  useListDeployTargets,
} from '@/lib/api/generated/deployments/deployments';
import { ifMatch } from '@/lib/api/idempotency';
import { TargetDialog } from './TargetDialog';
import { asConfig, configOf, targetSummary } from './targetForm';

/** Runs the connection test and offers to trust unknown SSH host keys. */
function TestDialog({ target, onClose, onChanged }: { target: DeployTarget | null; onClose: () => void; onChanged: () => Promise<unknown> }) {
  const { t } = useTranslation(['deploy', 'common']);
  const [result, setResult] = useState<{ id: string; res: TargetTestResult } | null>(null);
  const [busy, setBusy] = useState(false);
  const shown = target && result?.id === target.id ? result.res : null;

  const run = async (tg: DeployTarget) => {
    setBusy(true);
    try {
      setResult({ id: tg.id, res: await testDeployTarget(tg.id) });
      await onChanged(); // the list shows the last test's result
    } catch (err) {
      toast.error(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };

  // Pinning a fingerprint is a normal update of the target's config.
  const trust = async (tg: DeployTarget, host: string, fingerprint: string) => {
    const config = { ...configOf(tg) };
    if (tg.kind === 'ssh') config.host_keys = { ...((config.host_keys ?? {}) as Record<string, string>), [host]: fingerprint };
    else config.host_key = fingerprint;
    try {
      const updated = await updateDeployTarget(tg.id, { description: tg.description, config: asConfig(config) }, ifMatch(tg.version));
      toast.success(t('test.trusted'));
      await onChanged();
      await run(updated);
    } catch (err) {
      toast.error(errorMessage(err));
    }
  };

  return (
    <Dialog
      open={target !== null}
      onOpenChange={(o) => {
        if (!o) onClose();
      }}
    >
      <DialogContent className="sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>{t('test.title', { name: target?.name ?? '' })}</DialogTitle>
          <DialogDescription>{t('test.description')}</DialogDescription>
        </DialogHeader>
        {shown && (
          <ul className="divide-y rounded-md border" data-testid="test-result">
            {shown.checks.map((c) => (
              <li key={c.name} className="space-y-1 px-3 py-2 text-sm">
                <p className="flex items-center gap-2 font-medium">
                  {c.ok ? <CheckCircle2 aria-hidden className="text-success size-4" /> : <XCircle aria-hidden className="text-destructive size-4" />}
                  <span className="font-mono break-all" dir="ltr">
                    {c.name}
                  </span>
                </p>
                {c.detail && <p className="text-muted-foreground break-words">{c.detail}</p>}
                {c.fingerprint && (
                  <div className="flex flex-wrap items-center gap-2">
                    <code className="bg-muted rounded px-1.5 py-0.5 text-xs break-all" dir="ltr">
                      {c.fingerprint}
                    </code>
                    {!c.pinned && target && (
                      <Button size="sm" variant="outline" onClick={() => void trust(target, c.name, c.fingerprint ?? '')}>
                        <ShieldCheck aria-hidden />
                        {t('test.trust')}
                      </Button>
                    )}
                  </div>
                )}
              </li>
            ))}
          </ul>
        )}
        {shown && !shown.checks.some((c) => c.fingerprint && !c.pinned) && !shown.ok && (
          <p className="text-muted-foreground text-sm">{t('test.failedHint')}</p>
        )}
        {shown?.checks.some((c) => c.fingerprint && !c.pinned) && <p className="text-muted-foreground text-sm">{t('test.trustHint')}</p>}
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            {t('common:actions.close')}
          </Button>
          <Button
            disabled={busy || !target}
            onClick={() => {
              if (target) void run(target);
            }}
          >
            <Plug aria-hidden />
            {shown ? t('test.again') : t('test.run')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function LastTest({ target }: { target: DeployTarget }) {
  const { t } = useTranslation('deploy');
  const fmt = useFormat();
  if (target.last_test_at === null) {
    return (
      <span className="text-muted-foreground inline-flex items-center gap-1.5 text-sm">
        <CircleHelp aria-hidden className="size-4" />
        {t('targets.neverTested')}
      </span>
    );
  }
  return (
    <span className="inline-flex items-center gap-1.5 text-sm">
      {target.last_test_ok ? <CheckCircle2 aria-hidden className="text-success size-4" /> : <XCircle aria-hidden className="text-destructive size-4" />}
      {target.last_test_ok ? t('targets.testOk') : t('targets.testFailed')} · {fmt.relative(target.last_test_at)}
    </span>
  );
}

export function TargetsPage() {
  const { t } = useTranslation(['deploy', 'common']);
  const org = useCurrentOrg();
  const perms = usePermissions(org.id);
  const queryClient = useQueryClient();
  const canManage = perms.can(OrgAction.targetManage);
  const targets = useListDeployTargets(org.id);
  const [editing, setEditing] = useState<DeployTarget | 'new' | null>(null);
  const [testing, setTesting] = useState<DeployTarget | null>(null);
  const invalidate = () => queryClient.invalidateQueries({ queryKey: getListDeployTargetsQueryKey(org.id) });

  return (
    <div className="space-y-6">
      <PageHeader
        title={t('targets.title')}
        description={t('targets.description')}
        actions={
          canManage ? (
            <Button
              onClick={() => {
                setEditing('new');
              }}
            >
              <Plus aria-hidden />
              {t('targets.add')}
            </Button>
          ) : undefined
        }
      />
      {targets.isPending ? (
        <LoadingState />
      ) : targets.isError ? (
        <ErrorState error={targets.error} onRetry={() => void targets.refetch()} />
      ) : targets.data.items.length === 0 ? (
        <EmptyState title={t('targets.empty')} description={canManage ? t('targets.emptyManage') : undefined} />
      ) : (
        <Card className="gap-0 overflow-x-auto p-0">
          <Table data-testid="targets-table">
            <TableHeader>
              <TableRow>
                <TableHead>{t('targets.columns.name')}</TableHead>
                <TableHead>{t('targets.columns.kind')}</TableHead>
                <TableHead>{t('targets.columns.where')}</TableHead>
                <TableHead>{t('targets.columns.test')}</TableHead>
                {canManage && <TableHead className="w-0" />}
              </TableRow>
            </TableHeader>
            <TableBody>
              {targets.data.items.map((tg) => (
                <TableRow key={tg.id} data-testid="target-row">
                  <TableCell>
                    <span className="font-mono font-medium">{tg.name}</span>
                    {tg.description && <span className="text-muted-foreground block text-xs break-words">{tg.description}</span>}
                  </TableCell>
                  <TableCell>
                    <Badge variant="secondary">{t(`kinds.${tg.kind}`)}</Badge>
                  </TableCell>
                  <TableCell className="text-muted-foreground max-w-xs font-mono text-xs break-all" dir="ltr">
                    {targetSummary(tg)}
                  </TableCell>
                  <TableCell className="whitespace-nowrap">
                    <LastTest target={tg} />
                  </TableCell>
                  {canManage && (
                    <TableCell>
                      <div className="flex items-center justify-end gap-1">
                        <Button
                          variant="outline"
                          size="sm"
                          onClick={() => {
                            setTesting(tg);
                          }}
                        >
                          <Plug aria-hidden />
                          {t('test.button')}
                        </Button>
                        <Button
                          variant="ghost"
                          size="icon"
                          aria-label={t('targets.editFor', { name: tg.name })}
                          onClick={() => {
                            setEditing(tg);
                          }}
                        >
                          <Pencil aria-hidden />
                        </Button>
                        <ConfirmDialog
                          trigger={
                            <Button variant="ghost" size="icon" aria-label={t('targets.deleteFor', { name: tg.name })}>
                              <Trash2 aria-hidden />
                            </Button>
                          }
                          title={t('targets.deleteTitle', { name: tg.name })}
                          description={t('targets.deleteBody')}
                          confirmLabel={t('common:actions.delete')}
                          onConfirm={async () => {
                            try {
                              await deleteDeployTarget(tg.id);
                              toast.success(t('targets.deleted'));
                            } catch (err) {
                              toast.error(errorMessage(err));
                            }
                            await invalidate();
                          }}
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
      {editing !== null && (
        <TargetDialog
          key={editing === 'new' ? 'new' : `${editing.id}-${String(editing.version)}`}
          orgId={org.id}
          target={editing === 'new' ? undefined : editing}
          open
          onOpenChange={(o) => {
            if (!o) setEditing(null);
          }}
        />
      )}
      <TestDialog
        target={testing ? (targets.data?.items.find((x) => x.id === testing.id) ?? testing) : null}
        onClose={() => {
          setTesting(null);
        }}
        onChanged={invalidate}
      />
    </div>
  );
}
