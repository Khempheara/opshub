import { zodResolver } from '@hookform/resolvers/zod';
import { MailCheck } from 'lucide-react';
import { useState } from 'react';
import { useForm } from 'react-hook-form';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router';
import { z } from 'zod';
import { PasswordField, TextField } from '@/components/common/Field';
import { FormError } from '@/components/common/States';
import { Button } from '@/components/ui/button';
import { currentLocale } from '@/i18n';
import { applyFieldErrors, errorMessage, hasCode } from '@/lib/api/errors';
import { register as registerAccount } from '@/lib/api/generated/auth/auth';
import { useGetMeta } from '@/lib/api/generated/system/system';
import { getDisplayPrefs } from '@/preferences/store';
import { SSOButtons } from './SSOButtons';

const schema = z.object({
  display_name: z.string().trim().min(1, 'errors:rules.required').max(100, 'errors:rules.max'),
  email: z.string().trim().min(1, 'errors:rules.required').pipe(z.email('errors:rules.email')),
  password: z.string().min(12, 'errors:codes.PASSWORD_TOO_SHORT').max(128, 'errors:codes.PASSWORD_TOO_LONG'),
});
type Values = z.infer<typeof schema>;
const FIELDS = ['display_name', 'email', 'password'] as const;

export function RegisterPage() {
  const { t } = useTranslation(['auth', 'errors']);
  const meta = useGetMeta({ query: { staleTime: Infinity } });
  const [sentTo, setSentTo] = useState<string | null>(null);
  const [error, setError] = useState<unknown>(null);
  const form = useForm<Values>({ resolver: zodResolver(schema), defaultValues: { display_name: '', email: '', password: '' } });
  const { errors, isSubmitting } = form.formState;
  const msg = (m?: string) => (m ? t(m as 'errors:rules.required', { param: m === 'errors:rules.max' ? '100' : '' }) : undefined);

  const onSubmit = async (v: Values) => {
    setError(null);
    try {
      await registerAccount({ ...v, locale: currentLocale(), timezone: getDisplayPrefs().timeZone });
      setSentTo(v.email);
    } catch (err) {
      if (hasCode(err, 'PASSWORD_TOO_SHORT', 'PASSWORD_TOO_LONG', 'PASSWORD_BREACHED', 'PASSWORD_TOO_WEAK')) {
        form.setError('password', { type: 'server', message: errorMessage(err) });
        return;
      }
      if (!applyFieldErrors(err, form.setError, FIELDS)) setError(err);
    }
  };

  if (sentTo) {
    return (
      <div className="space-y-4 text-center">
        <MailCheck aria-hidden className="text-success mx-auto size-10" />
        <h1 className="text-2xl font-bold">{t('register.checkEmailTitle')}</h1>
        <p className="text-muted-foreground text-sm">{t('register.checkEmailBody', { email: sentTo })}</p>
        <Button asChild variant="outline" className="w-full">
          <Link to="/login">{t('register.signIn')}</Link>
        </Button>
      </div>
    );
  }

  if (meta.data?.signup_enabled === false) {
    return (
      <div className="space-y-4">
        <h1 className="text-2xl font-bold">{t('register.title')}</h1>
        <p className="text-muted-foreground text-sm">{t('register.disabled')}</p>
        <Button asChild className="w-full">
          <Link to="/login">{t('register.signIn')}</Link>
        </Button>
      </div>
    );
  }

  return (
    <div className="space-y-6">
      <div className="space-y-1">
        <h1 className="text-2xl font-bold">{t('register.title')}</h1>
        <p className="text-muted-foreground text-sm">{t('register.subtitle')}</p>
      </div>
      <SSOButtons next="/" />
      <form noValidate className="space-y-4" onSubmit={(e) => void form.handleSubmit(onSubmit)(e)}>
        <TextField label={t('fields.displayName')} autoComplete="name" error={msg(errors.display_name?.message)} {...form.register('display_name')} />
        <TextField label={t('fields.email')} type="email" autoComplete="email" inputMode="email" error={msg(errors.email?.message)} {...form.register('email')} />
        <PasswordField
          label={t('fields.password')}
          autoComplete="new-password"
          hint={t('passwordHint')}
          error={errors.password?.type === 'server' ? errors.password.message : msg(errors.password?.message)}
          {...form.register('password')}
        />
        <FormError error={error} />
        <Button type="submit" className="w-full" disabled={isSubmitting}>
          {t('register.submit')}
        </Button>
      </form>
      <p className="text-sm">
        {t('register.haveAccount')}{' '}
        <Link to="/login" className="text-primary font-medium underline-offset-4 hover:underline">
          {t('register.signIn')}
        </Link>
      </p>
    </div>
  );
}
