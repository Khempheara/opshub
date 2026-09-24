import { zodResolver } from '@hookform/resolvers/zod';
import { useQueryClient } from '@tanstack/react-query';
import { Plus } from 'lucide-react';
import { useState } from 'react';
import { useForm, useWatch } from 'react-hook-form';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';
import { z } from 'zod';
import { ConfirmDialog } from '@/components/common/ConfirmDialog';
import { CopyButton } from '@/components/common/CopyButton';
import { Field, TextField } from '@/components/common/Field';
import { PageHeader } from '@/components/common/PageHeader';
import { EmptyState, ErrorState, FormError, LoadingState } from '@/components/common/States';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card } from '@/components/ui/card';
import { Checkbox } from '@/components/ui/checkbox';
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from '@/components/ui/dialog';
import { Label } from '@/components/ui/label';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table';
import { useFormat } from '@/i18n/useFormat';
import { errorMessage } from '@/lib/api/errors';
import { createApiToken, getListApiTokensQueryKey, revokeApiToken, useListApiTokens } from '@/lib/api/generated/account/account';
import type { APIScope, CreatedAPIToken } from '@/lib/api/generated/model';

const EXPIRY_OPTIONS = ['30', '90', '365', 'never'] as const;

const schema = z.object({
  name: z.string().trim().min(1, 'errors:rules.required').max(100, 'errors:rules.max'),
  read: z.boolean(),
  write: z.boolean(),
  expiry: z.enum(EXPIRY_OPTIONS),
});
type Values = z.infer<typeof schema>;

function CreateTokenDialog({ onCreated }: { onCreated: (t: CreatedAPIToken) => void }) {
  const { t } = useTranslation(['settings', 'common', 'errors']);
  const [open, setOpen] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const form = useForm<Values>({ resolver: zodResolver(schema), defaultValues: { name: '', read: true, write: false, expiry: '90' } });
  const [read, write] = useWatch({ control: form.control, name: ['read', 'write'] });
  const nameError = form.formState.errors.name?.message;

  const onSubmit = async (v: Values) => {
    setError(null);
    const scopes: APIScope[] = [...(v.read ? (['api:read'] as const) : []), ...(v.write ? (['api:write'] as const) : [])];
    try {
      const created = await createApiToken({
        name: v.name,
        scopes,
        ...(v.expiry === 'never' ? {} : { expires_in_days: Number(v.expiry) }),
      });
      setOpen(false);
      form.reset();
      onCreated(created);
    } catch (err) {
      setError(err);
    }
  };

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button>
          <Plus aria-hidden />
          {t('tokens.create')}
        </Button>
      </DialogTrigger>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('tokens.createTitle')}</DialogTitle>
          <DialogDescription>{t('tokens.description')}</DialogDescription>
        </DialogHeader>
        <form noValidate className="space-y-4" onSubmit={(e) => void form.handleSubmit(onSubmit)(e)}>
          <TextField
            label={t('tokens.name')}
            placeholder={t('tokens.namePlaceholder')}
            error={nameError && t(nameError as 'errors:rules.required', { param: '100' })}
            {...form.register('name')}
          />
          <fieldset className="space-y-2">
            <legend className="text-sm font-medium">{t('tokens.scopes')}</legend>
            <div className="flex items-start gap-2">
              <Checkbox id="scope-read" checked={read} onCheckedChange={(v) => { form.setValue('read', v === true); }} className="mt-1" />
              <Label htmlFor="scope-read" className="font-normal">{t('tokens.scopeRead')}</Label>
            </div>
            <div className="flex items-start gap-2">
              <Checkbox id="scope-write" checked={write} onCheckedChange={(v) => { form.setValue('write', v === true); }} className="mt-1" />
              <Label htmlFor="scope-write" className="font-normal">{t('tokens.scopeWrite')}</Label>
            </div>
          </fieldset>
          <Field label={t('tokens.expiry')}>
            {({ id }) => (
              <select id={id} className="border-input bg-background block min-h-9 w-full rounded-md border px-3 py-1.5 text-sm" {...form.register('expiry')}>
                {EXPIRY_OPTIONS.map((o) => (
                  <option key={o} value={o}>
                    {o === 'never' ? t('tokens.expiryNever') : t('tokens.expiryDays', { count: Number(o) })}
                  </option>
                ))}
              </select>
            )}
          </Field>
          <FormError error={error} />
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => { setOpen(false); }}>
              {t('common:actions.cancel')}
            </Button>
            <Button type="submit" disabled={form.formState.isSubmitting || (!read && !write)}>
              {t('common:actions.create')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

export function TokensPage() {
  const { t } = useTranslation(['settings', 'common']);
  const fmt = useFormat();
  const queryClient = useQueryClient();
  const tokens = useListApiTokens({ limit: 100 });
  const [created, setCreated] = useState<CreatedAPIToken | null>(null);
  const invalidate = () => queryClient.invalidateQueries({ queryKey: getListApiTokensQueryKey() });

  return (
    <div className="space-y-6">
      <PageHeader
        title={t('tokens.title')}
        description={t('tokens.description')}
        actions={
          <CreateTokenDialog
            onCreated={(c) => {
              setCreated(c);
              void invalidate();
            }}
          />
        }
      />

      {tokens.isPending ? (
        <LoadingState />
      ) : tokens.isError ? (
        <ErrorState error={tokens.error} onRetry={() => void tokens.refetch()} />
      ) : tokens.data.items.length === 0 ? (
        <EmptyState title={t('tokens.empty')} />
      ) : (
        <Card className="gap-0 overflow-x-auto p-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t('tokens.columns.name')}</TableHead>
                <TableHead>{t('tokens.columns.prefix')}</TableHead>
                <TableHead>{t('tokens.columns.scopes')}</TableHead>
                <TableHead>{t('tokens.columns.lastUsed')}</TableHead>
                <TableHead>{t('tokens.columns.expires')}</TableHead>
                <TableHead className="w-0" />
              </TableRow>
            </TableHeader>
            <TableBody>
              {tokens.data.items.map((tok) => (
                <TableRow key={tok.id}>
                  <TableCell className="font-medium">{tok.name}</TableCell>
                  <TableCell className="font-mono text-xs">{tok.prefix}…</TableCell>
                  <TableCell className="space-x-1">
                    {tok.scopes.map((s) => (
                      <Badge key={s} variant="secondary" className="font-mono">
                        {s}
                      </Badge>
                    ))}
                  </TableCell>
                  <TableCell>{tok.last_used_at ? fmt.relative(tok.last_used_at) : t('common:time.never')}</TableCell>
                  <TableCell>{tok.expires_at ? fmt.dateTime(tok.expires_at, { dateStyle: 'medium' }) : t('tokens.expiryNever')}</TableCell>
                  <TableCell>
                    <ConfirmDialog
                      trigger={
                        <Button variant="outline" size="sm">
                          {t('common:actions.revoke')}
                        </Button>
                      }
                      title={t('tokens.revokeTitle', { name: tok.name })}
                      description={t('tokens.revokeBody')}
                      confirmLabel={t('common:actions.revoke')}
                      onConfirm={async () => {
                        try {
                          await revokeApiToken(tok.id);
                          toast.success(t('tokens.revoked'));
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

      <Dialog open={created !== null} onOpenChange={(open) => {
          if (!open) setCreated(null);
        }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t('tokens.createdTitle')}</DialogTitle>
            <DialogDescription>{t('tokens.createdBody')}</DialogDescription>
          </DialogHeader>
          {created && (
            <div className="space-y-3">
              <code className="bg-muted block rounded-md p-3 font-mono text-sm break-all" data-testid="new-token">
                {created.token}
              </code>
              <CopyButton value={created.token} />
            </div>
          )}
          <DialogFooter>
            <Button onClick={() => { setCreated(null); }}>{t('common:actions.done')}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
