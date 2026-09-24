import { zodResolver } from '@hookform/resolvers/zod';
import { useState } from 'react';
import { useForm } from 'react-hook-form';
import { useTranslation } from 'react-i18next';
import { Link, useNavigate, useSearchParams } from 'react-router';
import { z } from 'zod';
import { safeNext } from '@/app/navigation';
import { broadcastLogin, setSession } from '@/auth/session';
import { PasswordField, TextField } from '@/components/common/Field';
import { FormError } from '@/components/common/States';
import { Button } from '@/components/ui/button';
import { getPendingInvitation } from '@/features/org/pendingInvitation';
import { hasCode } from '@/lib/api/errors';
import { login, resendVerification } from '@/lib/api/generated/auth/auth';
import { useGetMeta } from '@/lib/api/generated/system/system';
import { SSOButtons } from './SSOButtons';

const schema = z.object({
  email: z.string().trim().min(1, 'errors:rules.required').pipe(z.email('errors:rules.email')),
  password: z.string().min(1, 'errors:rules.required'),
});
type Values = z.infer<typeof schema>;

export function LoginPage() {
  const { t } = useTranslation(['auth', 'errors']);
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const next = safeNext(params.get('next'));
  const ssoError = params.get('error');
  const meta = useGetMeta({ query: { staleTime: Infinity } });
  const [error, setError] = useState<unknown>(null);
  const [resent, setResent] = useState(false);

  const form = useForm<Values>({ resolver: zodResolver(schema), defaultValues: { email: '', password: '' } });
  const { errors, isSubmitting } = form.formState;

  const onSubmit = async (v: Values) => {
    setError(null);
    setResent(false);
    try {
      const res = await login(v);
      if (res.status === 'mfa_required' && res.mfa) {
        // The challenge travels in router state, never in the URL.
        await navigate(`/login/2fa?next=${encodeURIComponent(next)}`, { state: { mfaToken: res.mfa.token } });
        return;
      }
      if (res.tokens) {
        setSession(res.tokens);
        broadcastLogin();
        await navigate(next, { replace: true });
      }
    } catch (err) {
      setError(err);
    }
  };

  const lockedMinutes = hasCode(error, 'ACCOUNT_LOCKED') ? Math.ceil((error.retryAfterSeconds ?? 900) / 60) : null;

  return (
    <div className="space-y-6">
      <div className="space-y-1">
        <h1 className="text-2xl font-bold">{t('login.title')}</h1>
        <p className="text-muted-foreground text-sm">{t('login.subtitle')}</p>
      </div>

      {ssoError && <p role="alert" className="border-destructive/30 bg-destructive/5 text-destructive rounded-md border px-3 py-2 text-sm">{t(`errors:codes.${ssoError}` as 'errors:codes.SSO_FAILED', { defaultValue: t('errors:fallback') })}</p>}

      <SSOButtons next={next} />

      <form noValidate className="space-y-4" onSubmit={(e) => void form.handleSubmit(onSubmit)(e)}>
        <TextField
          label={t('fields.email')}
          type="email"
          autoComplete="username"
          inputMode="email"
          error={errors.email?.message && t(errors.email.message as 'errors:rules.required')}
          {...form.register('email')}
        />
        <PasswordField
          label={t('fields.password')}
          autoComplete="current-password"
          error={errors.password?.message && t(errors.password.message as 'errors:rules.required')}
          {...form.register('password')}
        />

        {lockedMinutes !== null ? (
          <p role="alert" className="border-destructive/30 bg-destructive/5 text-destructive rounded-md border px-3 py-2 text-sm">
            {t('login.lockedFor', { minutes: lockedMinutes })}
          </p>
        ) : hasCode(error, 'EMAIL_NOT_VERIFIED') ? (
          <div role="alert" className="space-y-2 rounded-md border px-3 py-2 text-sm">
            <p>{t('login.notVerified')}</p>
            {resent ? (
              <p className="text-success">{t('login.resent')}</p>
            ) : (
              <Button
                type="button"
                variant="link"
                className="h-auto p-0"
                onClick={() => {
                  void resendVerification({ email: form.getValues('email') })
                    .then(() => {
                      setResent(true);
                    })
                    .catch((err: unknown) => {
                      setError(err);
                    });
                }}
              >
                {t('login.resend')}
              </Button>
            )}
          </div>
        ) : (
          <FormError error={error} />
        )}

        <Button type="submit" className="w-full" disabled={isSubmitting}>
          {t('login.submit')}
        </Button>
      </form>

      <div className="flex flex-wrap justify-between gap-2 text-sm">
        <Link to="/forgot-password" className="text-primary underline-offset-4 hover:underline">
          {t('login.forgot')}
        </Link>
        {(meta.data?.signup_enabled !== false || getPendingInvitation() !== null) && (
          <span>
            {t('login.noAccount')}{' '}
            <Link to={next === '/' ? '/register' : `/register?next=${encodeURIComponent(next)}`} className="text-primary font-medium underline-offset-4 hover:underline">
              {t('login.register')}
            </Link>
          </span>
        )}
      </div>
    </div>
  );
}
