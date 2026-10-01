import { zodResolver } from '@hookform/resolvers/zod';
import { useMemo, useState } from 'react';
import { useForm, useWatch } from 'react-hook-form';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';
import { z } from 'zod';
import { saveProfile } from '@/auth/profile';
import { useSession } from '@/auth/session';
import { Field, TextField } from '@/components/common/Field';
import { PageHeader } from '@/components/common/PageHeader';
import { FormError } from '@/components/common/States';
import { Button } from '@/components/ui/button';
import { Card, CardContent } from '@/components/ui/card';
import { Checkbox } from '@/components/ui/checkbox';
import { Label } from '@/components/ui/label';
import { LOCALES } from '@/i18n';
import { formatDateTime, isValidTimeZone } from '@/i18n/format';
import { applyFieldErrors, hasCode } from '@/lib/api/errors';

const schema = z.object({
  display_name: z.string().trim().min(1, 'errors:rules.required').max(100, 'errors:rules.max'),
  locale: z.enum(LOCALES),
  timezone: z.string().refine(isValidTimeZone, 'errors:rules.timezone'),
  khmer_numerals: z.boolean(),
});
type Values = z.infer<typeof schema>;

const selectClass =
  'border-input bg-background focus-visible:border-ring focus-visible:ring-ring/50 block min-h-9 w-full rounded-md border px-3 py-1.5 text-sm outline-none focus-visible:ring-[3px]';

export function ProfilePage() {
  const { t } = useTranslation(['settings', 'common', 'errors']);
  const { user } = useSession();
  const [error, setError] = useState<unknown>(null);
  const timeZones = useMemo(() => Intl.supportedValuesOf('timeZone'), []);

  const form = useForm<Values>({
    resolver: zodResolver(schema),
    values: user
      ? { display_name: user.display_name, locale: user.locale, timezone: user.timezone, khmer_numerals: user.khmer_numerals }
      : undefined,
  });
  const { errors, isSubmitting, isDirty } = form.formState;
  const [locale, timezone, khmerNumerals] = useWatch({ control: form.control, name: ['locale', 'timezone', 'khmer_numerals'] });

  if (!user) return null;

  const onSubmit = async (v: Values) => {
    setError(null);
    try {
      await saveProfile(v);
      toast.success(t('profile.saved'));
    } catch (err) {
      if (hasCode(err, 'VERSION_CONFLICT')) toast.info(t('profile.conflict'));
      if (!applyFieldErrors(err, form.setError, ['display_name', 'locale', 'timezone'])) setError(err);
    }
  };

  const preview = isValidTimeZone(timezone)
    ? formatDateTime(new Date(), { locale, timeZone: timezone, khmerNumerals }, { dateStyle: 'full', timeStyle: 'short' })
    : '';

  return (
    <div className="space-y-6">
      <PageHeader title={t('profile.title')} description={t('profile.description')} />
      <Card className="max-w-xl">
        <CardContent>
          <form noValidate className="space-y-5" onSubmit={(e) => void form.handleSubmit(onSubmit)(e)}>
            <TextField
              label={t('profile.displayName')}
              autoComplete="name"
              error={errors.display_name?.message && t(errors.display_name.message as 'errors:rules.required', { param: '100' })}
              {...form.register('display_name')}
            />
            <TextField label={t('profile.email')} value={user.email} readOnly disabled />

            <Field label={t('profile.language')}>
              {({ id }) => (
                <select id={id} className={selectClass} {...form.register('locale')}>
                  {LOCALES.map((l) => (
                    <option key={l} value={l} lang={l}>
                      {t(`common:language.${l}`)}
                    </option>
                  ))}
                </select>
              )}
            </Field>

            <Field
              label={t('profile.timezone')}
              hint={t('profile.timezoneHint')}
              error={errors.timezone?.message && t(errors.timezone.message as 'errors:rules.timezone')}
            >
              {({ id, describedBy, invalid }) => (
                <select id={id} className={`${selectClass} font-mono`} aria-describedby={describedBy} aria-invalid={invalid || undefined} {...form.register('timezone')}>
                  {timeZones.map((tz) => (
                    <option key={tz} value={tz}>
                      {tz}
                    </option>
                  ))}
                </select>
              )}
            </Field>

            <div className="flex items-start gap-3">
              <Checkbox
                id="khmer-numerals"
                checked={khmerNumerals}
                onCheckedChange={(v) => {
                  form.setValue('khmer_numerals', v === true, { shouldDirty: true });
                }}
                className="mt-1"
              />
              <Label htmlFor="khmer-numerals" className="font-normal">
                {t('profile.khmerNumerals')}
              </Label>
            </div>

            {preview && <p className="text-muted-foreground text-sm">{t('profile.preview', { value: preview })}</p>}
            <FormError error={error} />
            <Button type="submit" disabled={isSubmitting || !isDirty}>
              {isSubmitting ? t('common:actions.saving') : t('common:actions.save')}
            </Button>
          </form>
        </CardContent>
      </Card>
    </div>
  );
}
