import { useTranslation } from 'react-i18next';
import { LOCALES, type Locale } from '@/i18n';
import { cn } from '@/lib/utils';

// Each language is labelled in its own script so it is recognizable regardless of the
// active UI language.
const SHORT_LABEL: Record<Locale, string> = { en: 'EN', km: 'ខ្មែរ' };

export function LanguageSwitcher() {
  const { t, i18n } = useTranslation();
  const active = i18n.resolvedLanguage;

  return (
    <div role="group" aria-label={t('language.label')} className="bg-muted inline-flex rounded-md p-0.5">
      {LOCALES.map((lng) => (
        <button
          key={lng}
          type="button"
          lang={lng}
          aria-pressed={active === lng}
          title={t(`language.${lng}`)}
          onClick={() => void i18n.changeLanguage(lng)}
          className={cn(
            'min-h-8 rounded px-2.5 text-sm font-medium transition-colors',
            active === lng ? 'bg-background shadow-xs' : 'text-muted-foreground hover:text-foreground',
          )}
        >
          {SHORT_LABEL[lng]}
        </button>
      ))}
    </div>
  );
}
