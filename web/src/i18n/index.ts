import i18n from 'i18next';
import LanguageDetector from 'i18next-browser-languagedetector';
import { initReactI18next } from 'react-i18next';

export const LOCALES = ['en', 'km'] as const;
export type Locale = (typeof LOCALES)[number];
export const DEFAULT_LOCALE: Locale = 'en';
export const LOCALE_STORAGE_KEY = 'opshub.locale';

export const isLocale = (v: unknown): v is Locale => LOCALES.includes(v as Locale);

// Every /locales/<lng>/<namespace>.json file is bundled; adding a namespace needs no wiring.
const modules = import.meta.glob<{ default: Record<string, unknown> }>('../locales/*/*.json', {
  eager: true,
});

export const resources: Record<string, Record<string, Record<string, unknown>>> = {};
for (const [file, mod] of Object.entries(modules)) {
  const match = /\/locales\/([^/]+)\/([^/]+)\.json$/.exec(file);
  if (!match) continue;
  const [, lng = '', ns = ''] = match;
  (resources[lng] ??= {})[ns] = mod.default;
}

export const NAMESPACES = Object.keys(resources[DEFAULT_LOCALE] ?? {});

function syncDocument(lng: string) {
  const locale = isLocale(lng) ? lng : DEFAULT_LOCALE;
  document.documentElement.lang = locale;
  document.documentElement.dir = 'ltr';
}

// Priority: user profile (applied after sign-in via changeLanguage) → saved choice →
// browser language → English.
void i18n
  .use(LanguageDetector)
  .use(initReactI18next)
  .init({
    resources,
    ns: NAMESPACES,
    defaultNS: 'common',
    fallbackLng: DEFAULT_LOCALE,
    supportedLngs: [...LOCALES],
    nonExplicitSupportedLngs: true,
    load: 'languageOnly',
    interpolation: { escapeValue: false }, // React already escapes
    returnNull: false,
    detection: {
      order: ['localStorage', 'navigator'],
      lookupLocalStorage: LOCALE_STORAGE_KEY,
      caches: ['localStorage'],
    },
  });

i18n.on('languageChanged', syncDocument);
syncDocument(i18n.resolvedLanguage ?? DEFAULT_LOCALE);

/** The active locale, narrowed to a supported one. */
export function currentLocale(): Locale {
  const lng = i18n.resolvedLanguage;
  return isLocale(lng) ? lng : DEFAULT_LOCALE;
}

export default i18n;
