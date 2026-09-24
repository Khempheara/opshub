import { zodResolver } from '@hookform/resolvers/zod';
import { useState } from 'react';
import { useForm } from 'react-hook-form';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router';
import { z } from 'zod';
import { TextField } from '@/components/common/Field';
import { FormError } from '@/components/common/States';
import { Button } from '@/components/ui/button';
import { forgotPassword } from '@/lib/api/generated/auth/auth';

const schema = z.object({ email: z.string().trim().min(1, 'errors:rules.required').pipe(z.email('errors:rules.email')) });
type Values = z.infer<typeof schema>;

export function ForgotPasswordPage() {
  const { t } = useTranslation(['auth', 'errors']);
  const [sentTo, setSentTo] = useState<string | null>(null);
  const [error, setError] = useState<unknown>(null);
  const form = useForm<Values>({ resolver: zodResolver(schema), defaultValues: { email: '' } });
  const emailError = form.formState.errors.email?.message;

  const onSubmit = async (v: Values) => {
    setError(null);
    try {
      await forgotPassword(v);
      setSentTo(v.email);
    } catch (err) {
      setError(err);
    }
  };

  return (
    <div className="space-y-6">
      <div className="space-y-1">
        <h1 className="text-2xl font-bold">{sentTo ? t('forgot.sentTitle') : t('forgot.title')}</h1>
        <p className="text-muted-foreground text-sm">{sentTo ? t('forgot.sentBody', { email: sentTo }) : t('forgot.subtitle')}</p>
      </div>
      {!sentTo && (
        <form noValidate className="space-y-4" onSubmit={(e) => void form.handleSubmit(onSubmit)(e)}>
          <TextField
            label={t('fields.email')}
            type="email"
            autoComplete="email"
            error={emailError && t(emailError as 'errors:rules.required')}
            {...form.register('email')}
          />
          <FormError error={error} />
          <Button type="submit" className="w-full" disabled={form.formState.isSubmitting}>
            {t('forgot.submit')}
          </Button>
        </form>
      )}
      <Link to="/login" className="text-primary text-sm underline-offset-4 hover:underline">
        {t('forgot.backToLogin')}
      </Link>
    </div>
  );
}
