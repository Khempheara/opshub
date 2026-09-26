import type audit from '@/locales/en/audit.json';
import type auth from '@/locales/en/auth.json';
import type common from '@/locales/en/common.json';
import type deploy from '@/locales/en/deploy.json';
import type errors from '@/locales/en/errors.json';
import type infra from '@/locales/en/infra.json';
import type home from '@/locales/en/home.json';
import type logs from '@/locales/en/logs.json';
import type monitoring from '@/locales/en/monitoring.json';
import type org from '@/locales/en/org.json';
import type pipeline from '@/locales/en/pipeline.json';
import type project from '@/locales/en/project.json';
import type runner from '@/locales/en/runner.json';
import type secret from '@/locales/en/secret.json';
import type settings from '@/locales/en/settings.json';

// English is the source of truth for keys: t('missing.key') is a type error.
// scripts/check-i18n.mjs guarantees Khmer has the same keys.
declare module 'i18next' {
  interface CustomTypeOptions {
    defaultNS: 'common';
    resources: {
      audit: typeof audit;
      auth: typeof auth;
      common: typeof common;
      deploy: typeof deploy;
      errors: typeof errors;
      home: typeof home;
      infra: typeof infra;
      logs: typeof logs;
      monitoring: typeof monitoring;
      org: typeof org;
      pipeline: typeof pipeline;
      project: typeof project;
      runner: typeof runner;
      secret: typeof secret;
      settings: typeof settings;
    };
  }
}
