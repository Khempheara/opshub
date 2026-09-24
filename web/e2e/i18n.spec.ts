import { expect, test } from './fixtures';

test.describe('language', () => {
  test('defaults to the browser language and falls back to English', async ({ browser }) => {
    const ctx = await browser.newContext({ locale: 'fr-FR' });
    const page = await ctx.newPage();
    await page.route('**/api/v1/meta', (r) => r.fulfill({ json: {} }));
    await page.goto('/');
    await expect(page.locator('html')).toHaveAttribute('lang', 'en');
    await ctx.close();
  });

  test('uses Khmer when the browser prefers it', async ({ browser }) => {
    const ctx = await browser.newContext({ locale: 'km-KH' });
    const page = await ctx.newPage();
    await page.route('**/api/v1/meta', (r) => r.fulfill({ json: {} }));
    await page.goto('/');
    await expect(page.locator('html')).toHaveAttribute('lang', 'km');
    await expect(page.getByRole('heading', { level: 1 })).toHaveText('សូមស្វាគមន៍មកកាន់ OpsHub');
    await ctx.close();
  });

  test('switcher changes language and the choice survives reload', async ({ page }) => {
    await page.goto('/');
    await expect(page.getByRole('heading', { level: 1 })).toHaveText('Welcome to OpsHub');

    await page.getByRole('button', { name: 'ខ្មែរ' }).click();
    await expect(page.locator('html')).toHaveAttribute('lang', 'km');
    await expect(page.getByRole('link', { name: 'ទំព័រដើម' })).toBeVisible();

    await page.reload();
    await expect(page.locator('html')).toHaveAttribute('lang', 'km');
    await expect(page.getByTestId('api-status')).toHaveText('កំពុងដំណើរការ');
    // Technical values stay as-is.
    await expect(page.getByText('កំណែ 1.2.3')).toBeVisible();
  });

  test('Khmer numerals preference', async ({ page }) => {
    await page.goto('/settings/preferences');
    await page.getByRole('button', { name: 'ខ្មែរ' }).click();
    await page.getByRole('checkbox').check();
    await page.getByRole('button', { name: 'រក្សាទុក' }).click();
    await expect(page.getByRole('status')).toHaveText('បានរក្សាទុកចំណូលចិត្ត។');

    await page.getByRole('link', { name: 'ទំព័រដើម' }).click();
    await expect(page.getByTestId('demo-number')).toHaveText(/^[០-៩,.]+$/);
  });

  test('API errors are translated by code', async ({ page }) => {
    await page.route('**/api/v1/meta', (r) =>
      r.fulfill({ status: 503, json: { error: { code: 'SERVICE_UNAVAILABLE', message: 'database unavailable', details: {} } } }),
    );
    await page.goto('/');
    await page.getByRole('button', { name: 'ខ្មែរ' }).click();
    await expect(page.getByRole('alert')).toHaveText('សេវាកម្មមិនអាចប្រើបានជាបណ្តោះអាសន្ន។', { timeout: 15_000 });
  });
});

test.describe('Khmer typography', () => {
  test('stacked consonants (ជើង) and vowels are not clipped', async ({ page }) => {
    await page.goto('/');
    await page.getByRole('button', { name: 'ខ្មែរ' }).click();
    await expect(page.locator('html')).toHaveAttribute('lang', 'km');
    await page.evaluate(() => document.fonts.ready);

    // For every element that clips its content and contains Khmer text, the content box
    // must not overflow vertically (overflow means glyph parts are cut off).
    const clipped = await page.evaluate(() => {
      const KHMER = /[ក-៿]/;
      const out: string[] = [];
      for (const el of Array.from(document.querySelectorAll<HTMLElement>('body *'))) {
        const cs = getComputedStyle(el);
        const clips = ['hidden', 'clip'].includes(cs.overflowY) || ['hidden', 'clip'].includes(cs.overflow);
        if (!clips || !KHMER.test(el.textContent)) continue;
        // Visually-hidden (sr-only) elements are 1×1px boxes that clip on purpose.
        if (el.clientWidth <= 1 && el.clientHeight <= 1) continue;
        if (el.scrollHeight > el.clientHeight + 1) out.push(`${el.tagName}.${el.className}: ${el.textContent.slice(0, 30)}`);
      }
      return out;
    });
    expect(clipped).toEqual([]);

    // Line boxes must be tall enough for subscripts: Khmer body line-height ≈ 1.7.
    const lineHeight = await page.evaluate(() => {
      const cs = getComputedStyle(document.body);
      return parseFloat(cs.lineHeight) / parseFloat(cs.fontSize);
    });
    expect(lineHeight).toBeGreaterThanOrEqual(1.6);

    // Every button's text fits inside the button.
    for (const btn of await page.getByRole('button').all()) {
      const fits = await btn.evaluate((b) => b.scrollHeight <= b.clientHeight + 1);
      expect(fits, await btn.innerText()).toBe(true);
    }
  });

  test('self-hosted Khmer fonts are loaded', async ({ page }) => {
    await page.goto('/');
    await page.getByRole('button', { name: 'ខ្មែរ' }).click();
    await page.evaluate(() => document.fonts.ready);
    const loaded = await page.evaluate(async () => {
      await document.fonts.load('16px "Noto Sans Khmer"', 'ក្ស');
      await document.fonts.load('600 16px "Kantumruy Pro"', 'ក្ស');
      return {
        body: document.fonts.check('16px "Noto Sans Khmer"', 'ក្ស'),
        heading: document.fonts.check('600 16px "Kantumruy Pro"', 'ក្ស'),
      };
    });
    expect(loaded).toEqual({ body: true, heading: true });
  });

  test('long Khmer text wraps instead of overflowing on mobile', async ({ page }) => {
    await page.setViewportSize({ width: 360, height: 800 });
    await page.goto('/');
    await page.getByRole('button', { name: 'ខ្មែរ' }).click();
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
    expect(overflow).toBeLessThanOrEqual(0);
  });
});
