import { expect, test, type Page } from '@playwright/test';
import { expectKhmerLayoutOK } from '../khmer';
import { newOwnerWithOrg, SEED_PASSWORD, signIn } from './helpers';

/** Opens a Logs tab and waits until it is shown (a closing dialog can swallow a click). */
async function openTab(page: Page, name: string, path: string) {
  await expect(async () => {
    await page.getByRole('navigation', { name: 'Logs sections' }).getByRole('link', { name, exact: true }).click();
    await expect(page).toHaveURL(new RegExp(`/logs${path}$`), { timeout: 2000 });
  }).toPass({ timeout: 15_000 });
}

function ndjson(lines: Record<string, unknown>[]): string {
  return lines.map((l) => JSON.stringify(l)).join('\n');
}

test('a service sends lines with an ingest token; they are searched, followed and the token is revoked', async ({ page }) => {
  test.setTimeout(120_000);
  const slug = await newOwnerWithOrg(page, 'Tonle Logs');
  await page.getByRole('link', { name: 'Logs' }).click();
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Logs');
  await expect(page.getByText('No log lines in this range')).toBeVisible();

  // An ingest token for one service; the name and service are validated.
  await openTab(page, 'Ingest tokens', '/tokens');
  await expect(page.getByText('No ingest tokens yet')).toBeVisible();
  await page.getByRole('button', { name: 'New ingest token' }).click();
  let dialog = page.getByRole('dialog');
  await dialog.getByLabel('Name').fill('Shop API');
  await dialog.getByLabel('Service').fill('Shop API');
  await dialog.getByRole('button', { name: 'Create token' }).click();
  await expect(dialog.getByText('Use lowercase letters, digits')).toBeVisible();
  await dialog.getByLabel('Service').fill('shop/api');
  await dialog.getByRole('button', { name: 'Create token' }).click();
  dialog = page.getByRole('dialog');
  await expect(dialog.getByRole('heading', { name: 'Ingest token created' })).toBeVisible();
  const token = (await dialog.getByTestId('ingest-token').innerText()).trim();
  expect(token).toMatch(/^ohl_[a-z2-7]{8}_/);
  await expect(dialog.locator('pre')).toContainText('/api/v1/ingest/logs');
  await dialog.getByRole('button', { name: 'Done' }).click();
  const row = page.getByTestId('ingest-token-row').filter({ hasText: 'Shop API' });
  await expect(row).toContainText('shop/api');
  await expect(row).toContainText('Never');

  // The service sends lines; bad ones are reported.
  const now = Date.now();
  const send = (body: string) =>
    page.request.post('/api/v1/ingest/logs', { data: body, headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/x-ndjson' } });
  let res = await send(
    [
      ndjson([
        { ts: new Date(now - 60_000).toISOString(), level: 'info', msg: 'GET /api/cart 200', path: '/api/cart', ms: 12 },
        { ts: new Date(now - 40_000).toISOString(), level: 'error', message: 'payment gateway timeout', order: 10233 },
        { ts: new Date(now - 20_000).toISOString(), level: 'warning', message: 'ការទូទាត់បរាជ័យសម្រាប់អតិថិជន' },
      ]),
      'not json',
    ].join('\n'),
  );
  expect(res.status()).toBe(200);
  expect(await res.json()).toEqual({ accepted: 3, rejected: 1, errors: [{ line: 4, rule: 'json_object' }] });
  await page.reload();
  await expect(row).not.toContainText('Never');

  // Search: everything, full text, Khmer, level.
  await openTab(page, 'Search', '');
  const lines = page.getByTestId('log-line');
  await expect(lines).toHaveCount(3);
  await expect(lines.first()).toContainText('ការទូទាត់បរាជ័យសម្រាប់អតិថិជន');
  await expect(lines.first()).toHaveAttribute('data-level', 'warn');
  await page.getByLabel('Search logs').fill('gateway');
  await page.getByRole('button', { name: 'Search', exact: true }).click();
  await expect(lines).toHaveCount(1);
  await expect(lines.first()).toContainText('payment gateway timeout');
  await page.getByLabel('Search logs').fill('cart');
  await page.getByRole('button', { name: 'Search', exact: true }).click();
  await expect(lines).toHaveCount(1);
  await expect(lines.first()).toContainText('GET /api/cart 200');
  await page.getByLabel('Search logs').fill('បរាជ័យ');
  await page.getByRole('button', { name: 'Search', exact: true }).click();
  await expect(lines).toHaveCount(1);
  await page.getByLabel('Search logs').fill('');
  await page.getByRole('button', { name: 'Search', exact: true }).click();
  await page.getByLabel('Minimum level').selectOption('error');
  await expect(lines).toHaveCount(1);
  // A line opens to show its attributes.
  await lines.first().getByRole('button').click();
  await expect(lines.first()).toContainText('order');
  await expect(lines.first()).toContainText('10233');
  await page.getByLabel('Minimum level').selectOption('');
  await expect(lines).toHaveCount(3);
  await expect(page.getByLabel('Service')).toContainText('shop/api');

  // Follow: new lines appear without reloading.
  await page.getByRole('button', { name: 'Follow' }).click();
  await expect(page.getByText('Following new lines')).toBeVisible();
  res = await send(ndjson([{ level: 'info', message: 'order 10234 paid' }]));
  expect(res.status()).toBe(200);
  await expect(lines.first()).toContainText('order 10234 paid', { timeout: 15_000 });
  await expect(lines).toHaveCount(4);
  await page.getByRole('button', { name: 'Follow' }).click();

  // Khmer layout on phone and desktop widths.
  await page.getByRole('button', { name: 'ខ្មែរ' }).click();
  for (const viewport of [
    { width: 375, height: 812 },
    { width: 1280, height: 800 },
  ]) {
    await page.setViewportSize(viewport);
    for (const path of ['', '/tokens']) {
      await page.goto(`/o/${slug}/logs${path}`);
      await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
      if (path === '') await expect(lines).toHaveCount(4);
      await expectKhmerLayoutOK(page);
    }
  }
  await page.getByRole('button', { name: 'EN', exact: true }).click();

  // A revoked token stops working; stored lines stay.
  await page.goto(`/o/${slug}/logs/tokens`);
  await row.getByRole('button', { name: 'Revoke Shop API' }).click();
  await page.getByRole('alertdialog').getByRole('button', { name: 'Revoke' }).click();
  await expect(page.getByText('Ingest token revoked')).toBeVisible();
  await expect(page.getByText('No ingest tokens yet')).toBeVisible();
  res = await send(ndjson([{ message: 'too late' }]));
  expect(res.status()).toBe(401);
  await openTab(page, 'Search', '');
  await expect(lines).toHaveCount(4);
});

test('viewers search logs but cannot manage ingest tokens', async ({ page }) => {
  await signIn(page, 'viewer@demo.opshub.local', SEED_PASSWORD);
  await page.getByRole('button', { name: 'EN', exact: true }).click();
  await page.goto('/o/angkor-tech/logs');
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Logs');
  await expect(page.getByRole('navigation', { name: 'Logs sections' })).toHaveCount(0);
  await page.getByLabel('Time range').selectOption('7d');
  await page.getByLabel('Service').selectOption('payments-api/checkout');
  await expect(page.getByTestId('log-line').first()).toBeVisible();
  await page.goto('/o/angkor-tech/logs/tokens');
  await expect(page.getByRole('heading', { level: 1 })).not.toHaveText('Logs');
});
