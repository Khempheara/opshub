import { zodResolver } from '@hookform/resolvers/zod';
import { useQueryClient } from '@tanstack/react-query';
import { Monitor } from 'lucide-react';
import { useState } from 'react';
import { useForm } from 'react-hook-form';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';
import { z } from 'zod';
import { useSession } from '@/auth/session';
import { ConfirmDialog } from '@/components/common/ConfirmDialog';
import { PasswordField } from '@/components/common/Field';
import { PageHeader } from '@/components/common/PageHeader';
import { EmptyState, ErrorState, FormError, LoadingState } from '@/components/common/States';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { AccountActivityCard } from '@/features/audit/AccountActivityCard';
import { useFormat } from '@/i18n/useFormat';
import { errorMessage, hasCode } from '@/lib/api/errors';
import {
  changePassword,
  getListIdentitiesQueryKey,
  getListSessionsQueryKey,
  revokeSession,
  unlinkIdentity,
  useListIdentities,
  useListSessions,
} from '@/lib/api/generated/account/account';
import { TwoFactorCard } from './TwoFactorCard';

const passwordSchema = z
  .object({
    current: z.string(),
    next: z.string().min(12, 'errors:codes.PASSWORD_TOO_SHORT').max(128, 'errors:codes.PASSWORD_TOO_LONG'),
    confirm: z.string(),
  })
  .refine((v) => v.next === v.confirm, { path: ['confirm'], message: 'auth:passwordMismatch' });
type PasswordValues = z.infer<typeof passwordSchema>;

function PasswordCard() {
  const { t } = useTranslation(['settings', 'auth', 'errors']);
  const { user } = useSession();
  const [error, setError] = useState<unknown>(null);
  const form = useForm<PasswordValues>({ resolver: zodResolver(passwordSchema), defaultValues: { current: '', next: '', confirm: '' } });
  const { errors, isSubmitting } = form.formState;
  const hasPassword = user?.has_password ?? true;

  const onSubmit = async (v: PasswordValues) => {
    setError(null);
    try {
      await changePassword({ current_password: v.current, new_password: v.next });
      form.reset();
      toast.success(t('security.password.changed'));
    } catch (err) {
      if (hasCode(err, 'PASSWORD_INCORRECT')) form.setError('current', { type: 'server', message: errorMessage(err) });
      else if (hasCode(err, 'PASSWORD_TOO_SHORT', 'PASSWORD_TOO_LONG', 'PASSWORD_BREACHED', 'PASSWORD_TOO_WEAK'))
        form.setError('next', { type: 'server', message: errorMessage(err) });
      else setError(err);
    }
  };
  const msg = (e?: { message?: string; type?: string }) =>
    e?.message ? (e.type === 'server' ? e.message : t(e.message as 'auth:passwordMismatch')) : undefined;

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t('security.password.title')}</CardTitle>
        <CardDescription>{hasPassword ? t('security.password.description') : t('security.password.ssoOnly')}</CardDescription>
      </CardHeader>
      <CardContent>
        <form noValidate className="max-w-md space-y-4" onSubmit={(e) => void form.handleSubmit(onSubmit)(e)}>
          <input type="email" autoComplete="username" value={user?.email ?? ''} readOnly hidden />
          {hasPassword && (
            <PasswordField label={t('security.password.current')} autoComplete="current-password" error={msg(errors.current)} {...form.register('current')} />
          )}
          <PasswordField label={t('auth:fields.newPassword')} autoComplete="new-password" hint={t('auth:passwordHint')} error={msg(errors.next)} {...form.register('next')} />
          <PasswordField label={t('auth:fields.confirmPassword')} autoComplete="new-password" error={msg(errors.confirm)} {...form.register('confirm')} />
          <FormError error={error} />
          <Button type="submit" disabled={isSubmitting}>
            {t('security.password.submit')}
          </Button>
        </form>
      </CardContent>
    </Card>
  );
}

function SessionsCard() {
  const { t } = useTranslation(['settings', 'common']);
  const fmt = useFormat();
  const queryClient = useQueryClient();
  const sessions = useListSessions({ limit: 50 });

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t('security.sessions.title')}</CardTitle>
        <CardDescription>{t('security.sessions.description')}</CardDescription>
      </CardHeader>
      <CardContent>
        {sessions.isPending ? (
          <LoadingState />
        ) : sessions.isError ? (
          <ErrorState error={sessions.error} onRetry={() => void sessions.refetch()} />
        ) : (
          <ul className="divide-y" data-testid="sessions">
            {sessions.data.items.map((s) => (
              <li key={s.id} className="flex flex-wrap items-center gap-3 py-3">
                <Monitor aria-hidden className="text-muted-foreground size-5 shrink-0" />
                <div className="min-w-0 flex-1 space-y-0.5">
                  <p className="truncate font-mono text-xs" title={s.user_agent ?? undefined}>
                    {s.user_agent ?? t('security.sessions.unknownDevice')}
                  </p>
                  <p className="text-muted-foreground text-xs">
                    <span className="font-mono">{s.ip}</span> · {t(`security.sessions.method.${s.auth_method}`)} ·{' '}
                    {t('security.sessions.lastActive', { time: fmt.relative(s.last_used_at) })}
                  </p>
                </div>
                {s.current ? (
                  <Badge variant="secondary">{t('security.sessions.current')}</Badge>
                ) : (
                  <ConfirmDialog
                    trigger={
                      <Button variant="outline" size="sm">
                        {t('security.sessions.revoke')}
                      </Button>
                    }
                    title={t('security.sessions.revokeTitle')}
                    description={t('security.sessions.revokeBody')}
                    confirmLabel={t('security.sessions.revoke')}
                    onConfirm={async () => {
                      try {
                        await revokeSession(s.id);
                        toast.success(t('security.sessions.revoked'));
                      } catch (err) {
                        toast.error(errorMessage(err));
                      }
                      await queryClient.invalidateQueries({ queryKey: getListSessionsQueryKey() });
                    }}
                  />
                )}
              </li>
            ))}
          </ul>
        )}
      </CardContent>
    </Card>
  );
}

function IdentitiesCard() {
  const { t } = useTranslation(['settings', 'auth', 'common']);
  const queryClient = useQueryClient();
  const identities = useListIdentities();
  const providerName = (p: string) => t(`auth:sso.providers.${p}` as 'auth:sso.providers.github', { defaultValue: p });

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t('security.identities.title')}</CardTitle>
        <CardDescription>{t('security.identities.description')}</CardDescription>
      </CardHeader>
      <CardContent>
        {identities.isPending ? (
          <LoadingState rows={1} />
        ) : identities.isError ? (
          <ErrorState error={identities.error} onRetry={() => void identities.refetch()} />
        ) : identities.data.items.length === 0 ? (
          <EmptyState title={t('security.identities.empty')} />
        ) : (
          <ul className="divide-y">
            {identities.data.items.map((i) => (
              <li key={i.id} className="flex flex-wrap items-center gap-3 py-3">
                <div className="min-w-0 flex-1">
                  <p className="font-medium">{providerName(i.provider)}</p>
                  {i.email && <p className="text-muted-foreground text-xs">{i.email}</p>}
                </div>
                <ConfirmDialog
                  trigger={
                    <Button variant="outline" size="sm">
                      {t('security.identities.unlink')}
                    </Button>
                  }
                  title={t('security.identities.unlinkTitle', { provider: providerName(i.provider) })}
                  description={t('security.identities.unlinkBody')}
                  confirmLabel={t('security.identities.unlink')}
                  onConfirm={async () => {
                    try {
                      await unlinkIdentity(i.id);
                      toast.success(t('security.identities.unlinked'));
                    } catch (err) {
                      toast.error(errorMessage(err));
                    }
                    await queryClient.invalidateQueries({ queryKey: getListIdentitiesQueryKey() });
                  }}
                />
              </li>
            ))}
          </ul>
        )}
      </CardContent>
    </Card>
  );
}

export function SecurityPage() {
  const { t } = useTranslation('settings');
  return (
    <div className="space-y-6">
      <PageHeader title={t('security.title')} />
      <PasswordCard />
      <TwoFactorCard />
      <SessionsCard />
      <IdentitiesCard />
      <AccountActivityCard />
    </div>
  );
}
