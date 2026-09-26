import { zodResolver } from '@hookform/resolvers/zod';
import { useQueryClient } from '@tanstack/react-query';
import { Plus, Trash2 } from 'lucide-react';
import { useState } from 'react';
import { useForm } from 'react-hook-form';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';
import { useCurrentOrg } from '@/app/org';
import { ConfirmDialog } from '@/components/common/ConfirmDialog';
import { CopyButton } from '@/components/common/CopyButton';
import { TextField } from '@/components/common/Field';
import { PageHeader } from '@/components/common/PageHeader';
import { EmptyState, ErrorState, FormError, LoadingState } from '@/components/common/States';
import { Button } from '@/components/ui/button';
import { Card } from '@/components/ui/card';
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from '@/components/ui/dialog';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table';
import { useFormat } from '@/i18n/useFormat';
import { applyFieldErrors, errorMessage } from '@/lib/api/errors';
import { createIngestToken, getListIngestTokensQueryKey, revokeIngestToken, useListIngestTokens } from '@/lib/api/generated/logs/logs';
import type { IngestToken } from '@/lib/api/generated/model';
import { LogsTabs } from './LogsTabs';
import { curlExample, tokenSchema, type TokenValues } from './logView';

/** Messages from zod are i18n keys; server field errors arrive translated. */
function useFieldMessage() {
  const { t } = useTranslation(['logs', 'errors']);
  return (m: string | undefined, param?: number) =>
    m && (m.startsWith('errors:') || m.startsWith('logs:') ? t(m as 'errors:rules.max', { param }) : m);
}

function CreateDialog({ orgId, onCreated }: { orgId: string; onCreated: (t: IngestToken) => void }) {
  const { t } = useTranslation(['logs', 'common']);
  const msg = useFieldMessage();
  const [open, setOpen] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const form = useForm<TokenValues>({ resolver: zodResolver(tokenSchema), defaultValues: { name: '', service: '' } });
  const errs = form.formState.errors;

  const onSubmit = async (v: TokenValues) => {
    setError(null);
    try {
      const token = await createIngestToken(orgId, { name: v.name, service: v.service });
      setOpen(false);
      form.reset();
      onCreated(token);
    } catch (err) {
      if (!applyFieldErrors(err, form.setError, ['name', 'service'])) setError(err);
    }
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        setOpen(o);
        if (o) setError(null);
      }}
    >
      <DialogTrigger asChild>
        <Button>
          <Plus aria-hidden />
          {t('tokens.add')}
        </Button>
      </DialogTrigger>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('tokens.addTitle')}</DialogTitle>
          <DialogDescription>{t('tokens.addDescription')}</DialogDescription>
        </DialogHeader>
        <form noValidate className="space-y-4" onSubmit={(e) => void form.handleSubmit(onSubmit)(e)}>
          <TextField label={t('tokens.name')} autoComplete="off" error={msg(errs.name?.message, 100)} {...form.register('name')} />
          <TextField
            label={t('tokens.service')}
            hint={t('tokens.serviceHint')}
            autoComplete="off"
            spellCheck={false}
            dir="ltr"
            error={msg(errs.service?.message)}
            {...form.register('service')}
          />
          <FormError error={error} />
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => {
                setOpen(false);
              }}
            >
              {t('common:actions.cancel')}
            </Button>
            <Button type="submit" disabled={form.formState.isSubmitting}>
              {t('tokens.create')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

/** Shows a new ingest token once, with a command that sends a test line. */
function CreatedDialog({ token, onClose }: { token: IngestToken | null; onClose: () => void }) {
  const { t } = useTranslation(['logs', 'common']);
  const example = token?.token ? curlExample(window.location.origin, token.token, token.service) : '';
  return (
    <Dialog
      open={token !== null}
      onOpenChange={(o) => {
        if (!o) onClose();
      }}
    >
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{t('tokens.createdTitle')}</DialogTitle>
          <DialogDescription>{t('tokens.createdBody')}</DialogDescription>
        </DialogHeader>
        {token?.token && (
          <div className="min-w-0 space-y-4">
            <div className="space-y-2">
              <code className="bg-muted block rounded-md p-3 font-mono text-sm break-all" data-testid="ingest-token">
                {token.token}
              </code>
              <CopyButton value={token.token} />
            </div>
            <div className="space-y-2">
              <p className="text-sm font-medium">{t('tokens.example')}</p>
              <pre className="bg-muted overflow-x-auto rounded-md p-3 font-mono text-xs" dir="ltr">
                {example}
              </pre>
              <CopyButton value={example} label={t('tokens.copyCommand')} />
              <p className="text-muted-foreground text-xs">{t('tokens.limits')}</p>
            </div>
          </div>
        )}
        <DialogFooter>
          <Button onClick={onClose}>{t('common:actions.done')}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/** Organization → Logs → Ingest tokens (logs.manage). */
export function IngestTokensPage() {
  const { t } = useTranslation(['logs', 'common']);
  const fmt = useFormat();
  const org = useCurrentOrg();
  const queryClient = useQueryClient();
  const tokens = useListIngestTokens(org.id);
  const [created, setCreated] = useState<IngestToken | null>(null);
  const invalidate = () => queryClient.invalidateQueries({ queryKey: getListIngestTokensQueryKey(org.id) });

  return (
    <div className="space-y-6">
      <PageHeader
        title={t('title')}
        description={t('tokens.description')}
        actions={
          <CreateDialog
            orgId={org.id}
            onCreated={(tok) => {
              setCreated(tok);
              void invalidate();
            }}
          />
        }
      />
      <LogsTabs />
      {tokens.isPending ? (
        <LoadingState />
      ) : tokens.isError ? (
        <ErrorState error={tokens.error} onRetry={() => void tokens.refetch()} />
      ) : tokens.data.items.length === 0 ? (
        <EmptyState title={t('tokens.empty')} description={t('tokens.emptyHint')} />
      ) : (
        <Card className="gap-0 overflow-x-auto p-0">
          <Table data-testid="ingest-tokens-table">
            <TableHeader>
              <TableRow>
                <TableHead>{t('tokens.columns.name')}</TableHead>
                <TableHead>{t('tokens.columns.service')}</TableHead>
                <TableHead>{t('tokens.columns.createdBy')}</TableHead>
                <TableHead>{t('tokens.columns.lastUsed')}</TableHead>
                <TableHead className="w-0" />
              </TableRow>
            </TableHeader>
            <TableBody>
              {tokens.data.items.map((tok) => (
                <TableRow key={tok.id} data-testid="ingest-token-row">
                  <TableCell className="max-w-64 whitespace-normal">
                    <span className="font-medium break-words">{tok.name}</span>
                    <span className="text-muted-foreground block font-mono text-xs">{tok.token_prefix}…</span>
                  </TableCell>
                  <TableCell className="font-mono text-sm" dir="ltr">
                    {tok.service}
                  </TableCell>
                  <TableCell className="text-sm">{tok.created_by_name ?? '—'}</TableCell>
                  <TableCell className="text-sm">{tok.last_used_at ? fmt.relative(tok.last_used_at) : t('tokens.never')}</TableCell>
                  <TableCell>
                    <ConfirmDialog
                      trigger={
                        <Button variant="ghost" size="icon" aria-label={t('tokens.revoke.buttonFor', { name: tok.name })}>
                          <Trash2 aria-hidden />
                        </Button>
                      }
                      title={t('tokens.revoke.title', { name: tok.name })}
                      description={t('tokens.revoke.body')}
                      confirmLabel={t('common:actions.revoke')}
                      onConfirm={async () => {
                        try {
                          await revokeIngestToken(tok.id);
                          toast.success(t('tokens.revoke.done'));
                        } catch (err) {
                          toast.error(errorMessage(err));
                        }
                        await invalidate();
                      }}
                    />
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Card>
      )}
      <CreatedDialog
        token={created}
        onClose={() => {
          setCreated(null);
        }}
      />
    </div>
  );
}
