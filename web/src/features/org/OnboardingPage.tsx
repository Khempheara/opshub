import { zodResolver } from '@hookform/resolvers/zod';
import { useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useForm } from 'react-hook-form';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router';
import { z } from 'zod';
import { rememberOrg } from '@/app/org';
import { TextField } from '@/components/common/Field';
import { FormError } from '@/components/common/States';
import { Button } from '@/components/ui/button';
import { Card } from '@/components/ui/card';
import { applyFieldErrors, errorMessage, hasCode } from '@/lib/api/errors';
import { createOrganization, getListOrganizationsQueryKey } from '@/lib/api/generated/organizations/organizations';
import { SLUG_PATTERN, slugify } from './slug';


const schema = z.object({
  name: z.string().trim().min(1, 'errors:rules.required').max(100, 'errors:rules.max'),
  slug: z.string().regex(SLUG_PATTERN, 'errors:rules.slug'),
});
type Values = z.infer<typeof schema>;

export function OnboardingPage() {
  const { t } = useTranslation(['home', 'errors']);
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [error, setError] = useState<unknown>(null);
  const [slugEdited, setSlugEdited] = useState(false);
  const form = useForm<Values>({ resolver: zodResolver(schema), defaultValues: { name: '', slug: '' } });
  const { errors, isSubmitting } = form.formState;

  const onSubmit = async (v: Values) => {
    setError(null);
    try {
      const org = await createOrganization(v);
      await queryClient.invalidateQueries({ queryKey: getListOrganizationsQueryKey() });
      rememberOrg(org.slug);
      await navigate(`/o/${org.slug}`);
    } catch (err) {
      if (hasCode(err, 'SLUG_TAKEN')) {
        form.setError('slug', { type: 'server', message: errorMessage(err) });
        return;
      }
      if (!applyFieldErrors(err, form.setError, ['name', 'slug'])) setError(err);
    }
  };

  const msg = (m?: string, type?: string) =>
    m ? (type === 'server' ? m : t(m as 'errors:rules.required', { param: '100' })) : undefined;

  return (
    <div className="mx-auto max-w-lg space-y-6">
      <div className="space-y-1">
        <h1 className="text-2xl font-bold">{t('onboarding.title')}</h1>
        <p className="text-muted-foreground text-sm">{t('onboarding.subtitle')}</p>
      </div>
      <Card>
        <form noValidate className="space-y-4" onSubmit={(e) => void form.handleSubmit(onSubmit)(e)}>
          <TextField
            label={t('onboarding.name')}
            error={msg(errors.name?.message, errors.name?.type)}
            {...form.register('name', {
              onChange: (e: { target: { value: string } }) => {
                if (!slugEdited) form.setValue('slug', slugify(e.target.value), { shouldValidate: form.formState.isSubmitted });
              },
            })}
          />
          <TextField
            label={t('onboarding.slug')}
            hint={t('onboarding.slugHint')}
            className="font-mono"
            autoCapitalize="none"
            spellCheck={false}
            error={msg(errors.slug?.message, errors.slug?.type)}
            {...form.register('slug', {
              onChange: () => {
                setSlugEdited(true);
              },
            })}
          />
          <FormError error={error} />
          <Button type="submit" disabled={isSubmitting}>
            {t('onboarding.submit')}
          </Button>
        </form>
      </Card>
    </div>
  );
}
