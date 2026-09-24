import { expect, type Page } from '@playwright/test';

/**
 * Khmer layout checks used by both suites:
 *  - no element that clips overflow has clipped Khmer text (stacked subscripts / vowels);
 *  - body line-height is tall enough for Khmer;
 *  - every button fits its text;
 *  - nothing overflows horizontally at the current viewport.
 */
export async function expectKhmerLayoutOK(page: Page) {
  await expect(page.locator('html')).toHaveAttribute('lang', 'km');
  await page.evaluate(() => document.fonts.ready);

  const clipped = await page.evaluate(() => {
    const KHMER = /[ក-៿]/;
    const out: string[] = [];
    for (const el of Array.from(document.querySelectorAll<HTMLElement>('body *'))) {
      const cs = getComputedStyle(el);
      const clips = ['hidden', 'clip'].includes(cs.overflowY) || ['hidden', 'clip'].includes(cs.overflow);
      if (!clips || !KHMER.test(el.textContent)) continue;
      if (el.clientWidth <= 1 && el.clientHeight <= 1) continue; // sr-only
      if (el.scrollHeight > el.clientHeight + 1) out.push(`${el.tagName}.${el.className}: ${el.textContent.slice(0, 30)}`);
    }
    return out;
  });
  expect(clipped, 'clipped Khmer text').toEqual([]);

  const lineHeight = await page.evaluate(() => {
    const cs = getComputedStyle(document.body);
    return parseFloat(cs.lineHeight) / parseFloat(cs.fontSize);
  });
  expect(lineHeight).toBeGreaterThanOrEqual(1.6);

  for (const btn of await page.getByRole('button').all()) {
    if (!(await btn.isVisible())) continue;
    const fits = await btn.evaluate((b) => b.scrollHeight <= b.clientHeight + 1);
    expect(fits, `button text clipped: ${await btn.innerText()}`).toBe(true);
  }

  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
  expect(overflow, 'horizontal overflow').toBeLessThanOrEqual(0);
}
