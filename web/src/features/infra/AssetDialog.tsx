import { useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router';
import { toast } from 'sonner';
import { Field, TextField } from '@/components/common/Field';
import { FormError } from '@/components/common/States';
import { Button } from '@/components/ui/button';
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog';
import { selectClass } from '@/features/org/constants';
import { fieldErrorMessage } from '@/lib/api/errors';
import { ApiError } from '@/lib/api/fetcher';
import type { Asset, AssetKind } from '@/lib/api/generated/model';
import { createAsset, getGetAssetQueryKey, getListAssetsQueryKey, updateAsset } from '@/lib/api/generated/infrastructure/infrastructure';
import { ifMatch } from '@/lib/api/idempotency';
import { emptyAssetForm, fromAsset, toRequest, type AssetForm } from './assetForm';

const KINDS: AssetKind[] = ['server', 'cluster', 'database', 'domain'];

const textareaClass =
  'border-input bg-background focus-visible:border-ring focus-visible:ring-ring/50 block w-full rounded-md border px-3 py-2 font-mono text-xs outline-none focus-visible:ring-[3px]';

/** Create (asset undefined) or edit an asset. */
export function AssetDialog({ orgId, orgSlug, asset, open, onOpenChange }: {
  orgId: string;
  orgSlug: string;
  asset?: Asset;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const { t } = useTranslation(['infra', 'common']);
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const [form, setForm] = useState<AssetForm>(() => (asset ? fromAsset(asset) : emptyAssetForm));
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const editing = asset !== undefined;
  const fieldErr = (name: string) => {
    if (!(error instanceof ApiError)) return undefined;
    const fe = error.fieldErrors.find((f) => f.field === name || f.field.startsWith(`${name}.`));
    return fe ? fieldErrorMessage(fe) : undefined;
  };
  const known = ['kind', 'name', 'address', 'description', 'tags', 'metadata', 'tls_port'];
  const unmatched = error !== null && !(error instanceof ApiError && error.fieldErrors.length > 0 && error.fieldErrors.every((f) => known.some((k) => f.field === k || f.field.startsWith(`${k}.`))));
  const set = (k: keyof AssetForm) => (v: string) => {
    setForm((f) => ({ ...f, [k]: v }));
  };

  const submit = async () => {
    setBusy(true);
    setError(null);
    try {
      if (editing) {
        await updateAsset(asset.id, toRequest(form, false), ifMatch(asset.version));
        await queryClient.invalidateQueries({ queryKey: getGetAssetQueryKey(asset.id) });
        toast.success(t('assets.saved'));
      } else {
        const created = await createAsset(orgId, toRequest(form, true));
        toast.success(t('assets.created'));
        await navigate(`/o/${orgSlug}/infrastructure/${created.id}`);
      }
      await queryClient.invalidateQueries({ queryKey: getListAssetsQueryKey(orgId) });
      onOpenChange(false);
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>{editing ? t('assets.editTitle', { name: asset.name }) : t('assets.createTitle')}</DialogTitle>
          <DialogDescription>{t('assets.formIntro')}</DialogDescription>
        </DialogHeader>
        <form
          noValidate
          className="space-y-4"
          onSubmit={(e) => {
            e.preventDefault();
            void submit();
          }}
        >
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label={t('assets.form.kind')} error={fieldErr('kind')}>
              {({ id }) =>
                editing ? (
                  <p id={id} className="py-1.5 text-sm">
                    {t(`kinds.${form.kind}`)}
                  </p>
                ) : (
                  <select
                    id={id}
                    className={selectClass}
                    value={form.kind}
                    onChange={(e) => {
                      set('kind')(e.target.value);
                    }}
                  >
                    {KINDS.map((k) => (
                      <option key={k} value={k}>
                        {t(`kinds.${k}`)}
                      </option>
                    ))}
                  </select>
                )
              }
            </Field>
            <TextField
              label={t('assets.form.name')}
              autoComplete="off"
              value={form.name}
              error={fieldErr('name')}
              onChange={(e) => {
                set('name')(e.target.value);
              }}
            />
          </div>
          <TextField
            label={t('assets.form.address')}
            hint={t(`assets.form.addressHint.${form.kind}`)}
            autoComplete="off"
            dir="ltr"
            value={form.address}
            error={fieldErr('address')}
            onChange={(e) => {
              set('address')(e.target.value);
            }}
          />
          {form.kind === 'domain' && (
            <TextField
              label={t('assets.form.tlsPort')}
              inputMode="numeric"
              value={form.tlsPort}
              error={fieldErr('tls_port')}
              onChange={(e) => {
                set('tlsPort')(e.target.value);
              }}
            />
          )}
          <TextField
            label={t('assets.form.description')}
            autoComplete="off"
            value={form.description}
            error={fieldErr('description')}
            onChange={(e) => {
              set('description')(e.target.value);
            }}
          />
          <TextField
            label={t('assets.form.tags')}
            hint={t('assets.form.tagsHint')}
            autoComplete="off"
            value={form.tags}
            error={fieldErr('tags')}
            onChange={(e) => {
              set('tags')(e.target.value);
            }}
          />
          <Field label={t('assets.form.metadata')} hint={t('assets.form.metadataHint')} error={fieldErr('metadata')}>
            {({ id, describedBy, invalid }) => (
              <textarea
                id={id}
                rows={3}
                dir="ltr"
                spellCheck={false}
                aria-describedby={describedBy}
                aria-invalid={invalid || undefined}
                className={textareaClass}
                value={form.metadata}
                onChange={(e) => {
                  set('metadata')(e.target.value);
                }}
              />
            )}
          </Field>
          {unmatched && <FormError error={error} />}
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => {
                onOpenChange(false);
              }}
            >
              {t('common:actions.cancel')}
            </Button>
            <Button type="submit" disabled={busy}>
              {editing ? t('common:actions.save') : t('common:actions.create')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
