import { expectKhmerLayoutOK } from '../khmer';
import { expect, test } from './fixtures';

test.describe('language', () => {
  test('follows the browser language and falls back to English', async ({ browser }) => {
    for (const [locale, lang, heading] of [
      ['fr-FR', 'en', 'Sign in to OpsHub'],
      ['km-KH', 'km', 'ចូលគណនី OpsHub'],
    ] as const) {
      const ctx = await browser.newContext({ locale });
      const page = await ctx.newPage();
      await page.route('**/api/v1/meta', (r) => r.fulfill({ json: {} }));
      await page.goto('/');
      await expect(page).toHaveURL(/\/login/);
      await expect(page.locator('html')).toHaveAttribute('lang', lang);
      await expect(page.getByRole('heading', { level: 1 })).toHaveText(heading);
      await ctx.close();
    }
  });

  test('switcher changes language and the choice survives reload', async ({ page }) => {
    await page.goto('/login');
    await page.getByRole('button', { name: 'ខ្មែរ' }).click();
    await expect(page.locator('html')).toHaveAttribute('lang', 'km');
    await page.reload();
    await expect(page.getByRole('heading', { level: 1 })).toHaveText('ចូលគណនី OpsHub');
    await expect(page.getByRole('link', { name: 'បន្តជាមួយ GitHub' })).toBeVisible();
  });

  test('API errors are translated by code', async ({ page }) => {
    await page.route('**/api/v1/auth/login', (r) =>
      r.fulfill({ status: 401, json: { error: { code: 'INVALID_CREDENTIALS', message: 'invalid email or password', details: {} } } }),
    );
    await page.goto('/login');
    await page.getByRole('button', { name: 'ខ្មែរ' }).click();
    await page.getByLabel('អ៊ីមែល').fill('dara@example.com');
    await page.getByLabel('ពាក្យសម្ងាត់', { exact: true }).fill('wrong-password');
    await page.getByRole('button', { name: 'ចូលគណនី', exact: true }).click();
    await expect(page.getByRole('alert')).toHaveText('អ៊ីមែល ឬពាក្យសម្ងាត់មិនត្រឹមត្រូវ។');
  });

  test('SSO errors returned in the URL are shown translated', async ({ page }) => {
    await page.goto('/login?error=SSO_EMAIL_UNVERIFIED');
    await expect(page.getByRole('alert')).toHaveText("The provider didn't confirm your email address.");
  });
});

test.describe('Khmer typography', () => {
  for (const path of ['/login', '/register', '/forgot-password']) {
    for (const width of [360, 768, 1280]) {
      test(`${path} at ${width}px`, async ({ page }) => {
        await page.setViewportSize({ width, height: 900 });
        await page.goto(path);
        await page.getByRole('button', { name: 'ខ្មែរ' }).click();
        await expectKhmerLayoutOK(page);
      });
    }
  }

  test('self-hosted Khmer fonts load (no CDN, CSP-safe)', async ({ page }) => {
    const external: string[] = [];
    page.on('request', (r) => {
      if (!r.url().startsWith('http://localhost:4173')) external.push(r.url());
    });
    await page.goto('/login');
    await page.getByRole('button', { name: 'ខ្មែរ' }).click();
    const loaded = await page.evaluate(async () => {
      await document.fonts.load('16px "Noto Sans Khmer"', 'ក្ស');
      await document.fonts.load('600 16px "Kantumruy Pro"', 'ក្ស');
      return document.fonts.check('16px "Noto Sans Khmer"', 'ក្ស') && document.fonts.check('600 16px "Kantumruy Pro"', 'ក្ស');
    });
    expect(loaded).toBe(true);
    expect(external).toEqual([]);
  });
});
