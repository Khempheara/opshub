import { useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';
import { Field, PasswordField, TextField } from '@/components/common/Field';
import { FormError } from '@/components/common/States';
import { Button } from '@/components/ui/button';
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog';
import { selectClass } from '@/features/org/constants';
import { ChannelKind, type NotificationChannel } from '@/lib/api/generated/model';
import { createNotificationChannel, getListNotificationChannelsQueryKey, updateNotificationChannel } from '@/lib/api/generated/monitoring/monitoring';
import { ifMatch } from '@/lib/api/idempotency';
import { channelForm, channelRequest, emptyChannelForm, fieldError, hasOtherErrors, type ChannelForm } from './forms';

const FIELDS = ['name', 'kind', 'config', 'secrets', 'locale'];
const LOCALES = [
  { value: 'en', label: 'English' },
  { value: 'km', label: 'ខ្មែរ' },
] as const;

/** Create (channel undefined) or edit a notification channel. Secrets are write-only. */
export function ChannelDialog({ orgId, channel, onClose }: { orgId: string; channel?: NotificationChannel; onClose: () => void }) {
  const { t } = useTranslation(['monitoring', 'common']);
  const queryClient = useQueryClient();
  const [form, setForm] = useState<ChannelForm>(() => (channel ? channelForm(channel) : emptyChannelForm));
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const editing = channel !== undefined;
  const set = <K extends keyof ChannelForm>(k: K, v: ChannelForm[K]) => {
    setForm((f) => ({ ...f, [k]: v }));
  };
  const err = (name: string) => fieldError(error, name);
  // On edit, stored secrets are kept when the field stays empty.
  const secretHint = (hint: string, name: string) => (editing && channel.secrets.includes(name) ? `${hint} ${t('channels.form.keepSecret')}` : hint);

  const submit = async () => {
    setBusy(true);
    setError(null);
    try {
      if (editing) {
        await updateNotificationChannel(channel.id, channelRequest(form, false), ifMatch(channel.version));
        toast.success(t('channels.saved'));
      } else {
        await createNotificationChannel(orgId, channelRequest(form, true));
        toast.success(t('channels.created'));
      }
      await queryClient.invalidateQueries({ queryKey: getListNotificationChannelsQueryKey(orgId) });
      onClose();
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog
      open
      onOpenChange={(o) => {
        if (!o) onClose();
      }}
    >
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{editing ? t('channels.editTitle', { name: channel.name }) : t('channels.createTitle')}</DialogTitle>
          <DialogDescription>{t('channels.intro')}</DialogDescription>
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
            <TextField
              label={t('channels.form.name')}
              autoComplete="off"
              value={form.name}
              error={err('name')}
              onChange={(e) => {
                set('name', e.target.value);
              }}
            />
            <Field label={t('channels.form.kind')} error={err('kind')}>
              {({ id }) =>
                editing ? (
                  <p id={id} className="py-1.5 text-sm">
                    {t(`channelKinds.${form.kind}`)}
                  </p>
                ) : (
                  <select
                    id={id}
                    className={selectClass}
                    value={form.kind}
                    onChange={(e) => {
                      set('kind', e.target.value as ChannelForm['kind']);
                    }}
                  >
                    {Object.values(ChannelKind).map((k) => (
                      <option key={k} value={k}>
                        {t(`channelKinds.${k}`)}
                      </option>
                    ))}
                  </select>
                )
              }
            </Field>
          </div>
          {form.kind === 'telegram' && (
            <>
              <TextField
                label={t('channels.form.chatId')}
                hint={t('channels.form.chatIdHint')}
                autoComplete="off"
                dir="ltr"
                value={form.chatId}
                error={err('config.chat_id')}
                onChange={(e) => {
                  set('chatId', e.target.value);
                }}
              />
              <PasswordField
                label={t('channels.form.botToken')}
                hint={secretHint(t('channels.form.botTokenHint'), 'bot_token')}
                autoComplete="off"
                dir="ltr"
                value={form.botToken}
                error={err('secrets.bot_token')}
                onChange={(e) => {
                  set('botToken', e.target.value);
                }}
              />
            </>
          )}
          {form.kind === 'slack' && (
            <PasswordField
              label={t('channels.form.webhookUrl')}
              hint={secretHint(t('channels.form.webhookUrlHint'), 'webhook_url')}
              autoComplete="off"
              dir="ltr"
              value={form.webhookUrl}
              error={err('secrets.webhook_url')}
              onChange={(e) => {
                set('webhookUrl', e.target.value);
              }}
            />
          )}
          {form.kind === 'email' && (
            <TextField
              label={t('channels.form.addresses')}
              hint={t('channels.form.addressesHint')}
              autoComplete="off"
              dir="ltr"
              value={form.addresses}
              error={err('config.addresses')}
              onChange={(e) => {
                set('addresses', e.target.value);
              }}
            />
          )}
          {form.kind === 'webhook' && (
            <>
              <TextField
                label={t('channels.form.url')}
                hint={t('channels.form.urlHint')}
                autoComplete="off"
                dir="ltr"
                value={form.url}
                error={err('config.url')}
                onChange={(e) => {
                  set('url', e.target.value);
                }}
              />
              <PasswordField
                label={t('channels.form.signingSecret')}
                hint={secretHint(t('channels.form.signingSecretHint'), 'signing_secret')}
                autoComplete="off"
                dir="ltr"
                value={form.signingSecret}
                error={err('secrets.signing_secret')}
                onChange={(e) => {
                  set('signingSecret', e.target.value);
                }}
              />
            </>
          )}
          <Field label={t('channels.form.locale')} error={err('locale')}>
            {({ id }) => (
              <select
                id={id}
                className={selectClass}
                value={form.locale}
                onChange={(e) => {
                  set('locale', e.target.value as ChannelForm['locale']);
                }}
              >
                <option value="">{t('channels.recipientLanguage')}</option>
                {LOCALES.map((l) => (
                  <option key={l.value} value={l.value} lang={l.value}>
                    {l.label}
                  </option>
                ))}
              </select>
            )}
          </Field>
          {hasOtherErrors(error, FIELDS) && <FormError error={error} />}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
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
