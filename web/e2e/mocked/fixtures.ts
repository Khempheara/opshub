import { test as base, expect } from '@playwright/test';

export const META = {
  version: '1.2.3',
  locales: ['en', 'km'],
  default_locale: 'en',
  default_timezone: 'Asia/Phnom_Penh',
  signup_enabled: true,
  sso_providers: ['github', 'keycloak'],
};

/** Every test gets a mocked /meta; anonymous visitors never call other endpoints on load. */
export const test = base.extend({
  page: async ({ page }, provide) => {
    await page.route('**/api/v1/meta', (route) => route.fulfill({ json: META }));
    await provide(page);
  },
});

export { expect };
