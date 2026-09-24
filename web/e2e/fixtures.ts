import { test as base, expect } from '@playwright/test';

export const META = { version: '1.2.3', locales: ['en', 'km'], default_locale: 'en', default_timezone: 'Asia/Phnom_Penh' };

/** Every test gets a healthy mocked API unless it overrides a route. */
export const test = base.extend({
  page: async ({ page }, provide) => {
    await page.route('**/api/v1/meta', (route) => route.fulfill({ json: META }));
    await provide(page);
  },
});

export { expect };
