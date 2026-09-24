import { zodResolver } from '@hookform/resolvers/zod';
import { useState } from 'react';
import { useForm } from 'react-hook-form';
import { useTranslation } from 'react-i18next';
import { z } from 'zod';
import { Button } from '@/components/ui/button';
import { Card } from '@/components/ui/card';
import { LOCALES } from '@/i18n';
import { isValidTimeZone } from '@/i18n/format';
import { setDisplayPrefs, useDisplayPrefs } from '@/preferences/store';

// Zod messages are translation keys, rendered with t() — never literal sentences.
const schema = z.object({
  locale: z.enum(LOCALES),
  timeZone: z.string().trim().min(1, 'errors:rules.required').refine(isValidTimeZone, 'settings:preferences.invalidTimezone'),
  khmerNumerals: z.boolean(),
});
type FormValues = z.infer<typeof schema>;

export function PreferencesPage() {
  const { t, i18n } = useTranslation(['settings', 'common', 'errors']);
  const prefs = useDisplayPrefs();
  const [saved, setSaved] = useState(false);

  const form = useForm<FormValues>({
    resolver: zodResolver(schema),
    values: {
      locale: i18n.resolvedLanguage === 'km' ? 'km' : 'en',
      timeZone: prefs.timeZone,
      khmerNumerals: prefs.khmerNumerals,
    },
  });
  const { errors } = form.formState;

  const onSubmit = async (v: FormValues) => {
    setDisplayPrefs({ timeZone: v.timeZone, khmerNumerals: v.khmerNumerals });
    await i18n.changeLanguage(v.locale);
    setSaved(true);
  };

  return (
    <div className="max-w-xl space-y-6">
      <div className="space-y-1">
        <h1 className="text-2xl font-bold">{t('settings:preferences.title')}</h1>
        <p className="text-muted-foreground text-sm">{t('settings:preferences.description')}</p>
      </div>

      <Card>
        <form
          noValidate
          className="space-y-5"
          onSubmit={(e) => void form.handleSubmit(onSubmit)(e)}
          onChange={() => {
            setSaved(false);
          }}
        >
          <div className="space-y-1.5">
            <label htmlFor="locale" className="text-sm font-medium">
              {t('settings:preferences.language')}
            </label>
            <select id="locale" className="border-input bg-background block w-full rounded-md border px-3 py-2" {...form.register('locale')}>
              {LOCALES.map((l) => (
                <option key={l} value={l} lang={l}>
                  {t(`common:language.${l}`)}
                </option>
              ))}
            </select>
          </div>

          <div className="space-y-1.5">
            <label htmlFor="timeZone" className="text-sm font-medium">
              {t('settings:preferences.timezone')}
            </label>
            <input
              id="timeZone"
              className="border-input bg-background block w-full rounded-md border px-3 py-2 font-mono"
              aria-invalid={!!errors.timeZone}
              aria-describedby="timeZone-hint timeZone-error"
              {...form.register('timeZone')}
            />
            <p id="timeZone-hint" className="text-muted-foreground text-xs">
              {t('settings:preferences.timezoneHint')}
            </p>
            {errors.timeZone?.message && (
              <p id="timeZone-error" role="alert" className="text-destructive text-sm">
                {t(errors.timeZone.message as 'settings:preferences.invalidTimezone')}
              </p>
            )}
          </div>

          <label className="flex items-start gap-3 text-sm">
            <input type="checkbox" className="mt-1.5 size-4" {...form.register('khmerNumerals')} />
            <span>{t('settings:preferences.khmerNumerals')}</span>
          </label>

          <div className="flex items-center gap-4">
            <Button type="submit" disabled={form.formState.isSubmitting}>
              {t('common:actions.save')}
            </Button>
            {saved && (
              <p role="status" className="text-success text-sm">
                {t('settings:preferences.saved')}
              </p>
            )}
          </div>
        </form>
      </Card>
    </div>
  );
}
