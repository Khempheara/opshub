import type auth from '@/locales/en/auth.json';
import type common from '@/locales/en/common.json';
import type errors from '@/locales/en/errors.json';
import type home from '@/locales/en/home.json';
import type org from '@/locales/en/org.json';
import type pipeline from '@/locales/en/pipeline.json';
import type project from '@/locales/en/project.json';
import type runner from '@/locales/en/runner.json';
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
      org: typeof org;
      pipeline: typeof pipeline;
      project: typeof project;
      runner: typeof runner;
      settings: typeof settings;
    };
  }
}
