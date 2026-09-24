import { useState } from 'react';
import { useForm } from 'react-hook-form';
import { useTranslation } from 'react-i18next';
import { Link, useLocation, useNavigate, useSearchParams } from 'react-router';
import { safeNext } from '@/app/navigation';
import { broadcastLogin, setSession } from '@/auth/session';
import { TextField } from '@/components/common/Field';
import { FormError } from '@/components/common/States';
import { Button } from '@/components/ui/button';
import { hasCode } from '@/lib/api/errors';
import { loginTwoFactor } from '@/lib/api/generated/auth/auth';

interface Values {
  code: string;
}

/** Second step of sign-in: TOTP code or recovery code. */
export function TwoFactorPage() {
  const { t } = useTranslation(['auth', 'errors']);
  const navigate = useNavigate();
  const location = useLocation();
  const [params] = useSearchParams();
  const next = safeNext(params.get('next'));
  const viaSSO = params.get('sso') === '1';
  const mfaToken = (location.state as { mfaToken?: string } | null)?.mfaToken;
  const [recovery, setRecovery] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const form = useForm<Values>({ defaultValues: { code: '' } });

  if (!mfaToken && !viaSSO) {
    return (
      <div className="space-y-4">
        <h1 className="text-2xl font-bold">{t('twoFactor.title')}</h1>
        <p className="text-muted-foreground text-sm">{t('twoFactor.expired')}</p>
        <Button asChild className="w-full">
          <Link to="/login">{t('twoFactor.backToLogin')}</Link>
        </Button>
      </div>
    );
  }

  const onSubmit = async ({ code }: Values) => {
    setError(null);
    try {
      const tokens = await loginTwoFactor({
        ...(mfaToken ? { mfa_token: mfaToken } : {}),
        ...(recovery ? { recovery_code: code.trim() } : { code: code.replace(/\s/g, '') }),
      });
      setSession(tokens);
      broadcastLogin();
      await navigate(next, { replace: true });
    } catch (err) {
      if (hasCode(err, 'MFA_CHALLENGE_EXPIRED')) {
        await navigate('/login', { replace: true });
        return;
      }
      setError(err);
      form.setValue('code', '');
      form.setFocus('code');
    }
  };

  return (
    <div className="space-y-6">
      <div className="space-y-1">
        <h1 className="text-2xl font-bold">{t('twoFactor.title')}</h1>
        <p className="text-muted-foreground text-sm">{recovery ? t('twoFactor.recoverySubtitle') : t('twoFactor.subtitle')}</p>
      </div>
      <form noValidate className="space-y-4" onSubmit={(e) => void form.handleSubmit(onSubmit)(e)}>
        {recovery ? (
          <TextField key="recovery" label={t('fields.recoveryCode')} autoComplete="off" autoFocus className="font-mono" {...form.register('code', { required: true })} />
        ) : (
          <TextField
            key="totp"
            label={t('fields.code')}
            inputMode="numeric"
            autoComplete="one-time-code"
            maxLength={7}
            autoFocus
            className="font-mono tracking-widest"
            {...form.register('code', { required: true })}
          />
        )}
        <FormError error={error} />
        <Button type="submit" className="w-full" disabled={form.formState.isSubmitting}>
          {t('twoFactor.submit')}
        </Button>
      </form>
      <Button
        type="button"
        variant="link"
        className="h-auto p-0"
        onClick={() => {
          setRecovery((r) => !r);
          setError(null);
          form.reset();
        }}
      >
        {recovery ? t('twoFactor.useCode') : t('twoFactor.useRecovery')}
      </Button>
    </div>
  );
}
