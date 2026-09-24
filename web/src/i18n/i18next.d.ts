import type auth from '@/locales/en/auth.json';
import type common from '@/locales/en/common.json';
import type errors from '@/locales/en/errors.json';
import type home from '@/locales/en/home.json';
import type settings from '@/locales/en/settings.json';

// English is the source of truth for keys: t('missing.key') is a type error.
// scripts/check-i18n.mjs guarantees Khmer has the same keys.
declare module 'i18next' {
  interface CustomTypeOptions {
    defaultNS: 'common';
    resources: {
      auth: typeof auth;
      common: typeof common;
      errors: typeof errors;
      home: typeof home;
      settings: typeof settings;
    };
  }
}
