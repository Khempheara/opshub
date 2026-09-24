import { zodResolver } from '@hookform/resolvers/zod';
import { CheckCircle2 } from 'lucide-react';
import { useState } from 'react';
import { useForm } from 'react-hook-form';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router';
import { z } from 'zod';
import { PasswordField } from '@/components/common/Field';
import { FormError } from '@/components/common/States';
import { Button } from '@/components/ui/button';
import { errorMessage, hasCode } from '@/lib/api/errors';
import { resetPassword } from '@/lib/api/generated/auth/auth';
import { useHashToken } from './tokenFromHash';

const schema = z
  .object({
    password: z.string().min(12, 'errors:codes.PASSWORD_TOO_SHORT').max(128, 'errors:codes.PASSWORD_TOO_LONG'),
    confirm: z.string(),
  })
  .refine((v) => v.password === v.confirm, { path: ['confirm'], message: 'auth:passwordMismatch' });
type Values = z.infer<typeof schema>;

export function ResetPasswordPage() {
  const { t } = useTranslation(['auth', 'errors']);
  // Read once: the fragment is removed from the address bar right after.
  const token = useHashToken();
  const [done, setDone] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const form = useForm<Values>({ resolver: zodResolver(schema), defaultValues: { password: '', confirm: '' } });
  const { errors, isSubmitting } = form.formState;

  if (!token) {
    return (
      <div className="space-y-4">
        <h1 className="text-2xl font-bold">{t('reset.title')}</h1>
        <p className="text-muted-foreground text-sm">{t('verify.missingToken')}</p>
        <Button asChild className="w-full">
          <Link to="/forgot-password">{t('forgot.submit')}</Link>
        </Button>
      </div>
    );
  }

  const onSubmit = async (v: Values) => {
    setError(null);
    try {
      await resetPassword({ token, password: v.password });
      setDone(true);
    } catch (err) {
      if (hasCode(err, 'PASSWORD_TOO_SHORT', 'PASSWORD_TOO_LONG', 'PASSWORD_BREACHED', 'PASSWORD_TOO_WEAK')) {
        form.setError('password', { type: 'server', message: errorMessage(err) });
        return;
      }
      setError(err);
    }
  };

  if (done) {
    return (
      <div className="space-y-4">
        <h1 className="text-2xl font-bold">{t('reset.title')}</h1>
        <p role="status" className="flex items-start gap-2 text-sm">
          <CheckCircle2 aria-hidden className="text-success mt-1 size-4 shrink-0" />
          {t('reset.success')}
        </p>
        <Button asChild className="w-full">
          <Link to="/login">{t('verify.goToLogin')}</Link>
        </Button>
      </div>
    );
  }

  const msg = (m?: string, type?: string) => (m ? (type === 'server' ? m : t(m as 'auth:passwordMismatch')) : undefined);
  return (
    <div className="space-y-6">
      <h1 className="text-2xl font-bold">{t('reset.title')}</h1>
      <form noValidate className="space-y-4" onSubmit={(e) => void form.handleSubmit(onSubmit)(e)}>
        <PasswordField
          label={t('fields.newPassword')}
          autoComplete="new-password"
          hint={t('passwordHint')}
          error={msg(errors.password?.message, errors.password?.type)}
          {...form.register('password')}
        />
        <PasswordField label={t('fields.confirmPassword')} autoComplete="new-password" error={msg(errors.confirm?.message)} {...form.register('confirm')} />
        <FormError error={error} />
        <Button type="submit" className="w-full" disabled={isSubmitting}>
          {t('reset.submit')}
        </Button>
      </form>
    </div>
  );
}
