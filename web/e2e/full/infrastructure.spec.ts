import { expect, test } from '@playwright/test';
import { expectKhmerLayoutOK } from '../khmer';
import { newOwnerWithOrg, SEED_PASSWORD, signIn, switchLanguage } from './helpers';

test('inventory, agent token and heartbeat, metrics, certificates', async ({ page }) => {
  test.setTimeout(120_000);
  const slug = await newOwnerWithOrg(page, 'Tonle Infra');
  await page.goto(`/o/${slug}/infrastructure`);
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Infrastructure');
  await expect(page.getByText('No assets yet')).toBeVisible();

  // Create a server: bad tags are refused by the server and shown on the field.
  await page.getByRole('button', { name: 'Add asset' }).click();
  let dialog = page.getByRole('dialog');
  await dialog.getByLabel('Name').fill('web-1');
  await dialog.getByLabel('Address').fill('10.0.0.12');
  await dialog.getByLabel('Tags').fill('prod, Bad Tag!');
  await dialog.getByLabel('Metadata').fill('provider=hetzner');
  await dialog.getByRole('button', { name: 'Create' }).click();
  await expect(dialog.getByText('This value has an invalid format.')).toBeVisible();
  await dialog.getByLabel('Tags').fill('prod, web');
  await dialog.getByRole('button', { name: 'Create' }).click();
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('web-1');
  await expect(page.locator('[data-status]').first()).toHaveText('No agent');
  const assetUrl = page.url();
  const assetId = assetUrl.split('/').pop() ?? '';

  // Edit it.
  await page.getByRole('button', { name: 'Edit' }).click();
  dialog = page.getByRole('dialog');
  await dialog.getByLabel('Description').fill('Payments API');
  await dialog.getByRole('button', { name: 'Save' }).click();
  await expect(page.getByText('Payments API')).toBeVisible();
  await expect(page.getByText('hetzner')).toBeVisible();

  // Agent token: shown once with the commands; the agent's side reports a heartbeat.
  await page.getByRole('button', { name: 'Create agent token' }).click();
  const token = (await page.getByTestId('agent-token').textContent()) ?? '';
  expect(token).toMatch(/^ohi_/);
  await expect(page.getByRole('dialog')).toContainText('opshub-runner agent --url');
  await page.getByRole('button', { name: 'Done' }).click();
  await expect(page.getByText('Waiting for the first heartbeat')).toBeVisible();

  const hb = await page.request.post('/api/v1/agent/heartbeat', {
    headers: { Authorization: `Bearer ${token}` },
    data: { version: '1.2.3', hostname: 'web-1.internal', os: 'linux', arch: 'amd64', cpu_pct: 42, mem_pct: 61, disk_pct: 77, load1: 0.5 },
  });
  expect(hb.status()).toBe(200);
  expect(((await hb.json()) as { interval_seconds: number }).interval_seconds).toBe(30);
  const bad = await page.request.post('/api/v1/agent/heartbeat', { headers: { Authorization: 'Bearer ohi_nope_x' }, data: {} });
  expect(bad.status()).toBe(401);

  await page.reload();
  await expect(page.locator('[data-status]').first()).toHaveText('Online');
  await expect(page.getByText('web-1.internal')).toBeVisible();
  await expect(page.getByText('linux/amd64 · 1.2.3')).toBeVisible();
  await page.getByRole('button', { name: '1 hour' }).click();
  await expect(page.getByRole('img', { name: /^CPU: Average 42%/ })).toBeVisible();

  // Rotating the token revokes the old one.
  await page.getByRole('button', { name: 'Rotate token' }).click();
  await page.getByRole('alertdialog').getByRole('button', { name: 'Rotate token' }).click();
  const token2 = (await page.getByTestId('agent-token').textContent()) ?? '';
  expect(token2).not.toBe(token);
  await page.getByRole('button', { name: 'Done' }).click();
  const old = await page.request.post('/api/v1/agent/heartbeat', { headers: { Authorization: `Bearer ${token}` }, data: {} });
  expect(old.status()).toBe(401);

  // The list shows usage bars; filters work.
  await page.goto(`/o/${slug}/infrastructure`);
  await expect(page.getByTestId('asset-row')).toContainText('web-1');
  await expect(page.getByTestId('asset-row')).toContainText('42%');

  // A domain whose certificate check fails (".invalid" never resolves).
  await page.getByRole('button', { name: 'Add asset' }).click();
  dialog = page.getByRole('dialog');
  await dialog.getByLabel('Kind').selectOption('domain');
  await dialog.getByLabel('Name').fill('shop');
  await dialog.getByLabel('Address').fill('not a domain');
  await dialog.getByRole('button', { name: 'Create' }).click();
  await expect(dialog.getByText('Enter a domain name, e.g. shop.example.com.')).toBeVisible();
  await dialog.getByLabel('Address').fill('shop.opshub.invalid');
  await dialog.getByRole('button', { name: 'Create' }).click();
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('shop');
  await page.getByRole('button', { name: 'Check now' }).click();
  await expect(page.getByText(/^connection failed/).first()).toBeVisible({ timeout: 20_000 });

  await page.getByRole('link', { name: 'All assets' }).click();
  await page.getByLabel('Kind').selectOption('domain');
  await expect(page.getByTestId('asset-row')).toHaveCount(1);
  await expect(page.getByTestId('asset-row')).toContainText('shop');
  await page.getByRole('link', { name: 'Certificates' }).click();
  const certRow = page.getByTestId('certificate-row');
  await expect(certRow).toContainText('shop.opshub.invalid');
  await expect(certRow).toContainText('Check failed');
  // Failing checks count as "needs attention" in every window.
  await page.getByLabel('Show').selectOption('7d');
  await expect(certRow).toHaveCount(1);

  // Khmer layout on phone and desktop widths.
  await switchLanguage(page, 'km');
  for (const viewport of [
    { width: 375, height: 812 },
    { width: 1280, height: 800 },
  ]) {
    await page.setViewportSize(viewport);
    for (const path of [`/o/${slug}/infrastructure`, `/o/${slug}/infrastructure/certificates`, `/o/${slug}/infrastructure/${assetId}`]) {
      await page.goto(path);
      await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
      await expectKhmerLayoutOK(page);
    }
  }
  await switchLanguage(page, 'en');

  // Delete.
  await page.goto(assetUrl);
  await page.getByRole('button', { name: 'Delete' }).click();
  await page.getByRole('alertdialog').getByRole('button', { name: 'Delete' }).click();
  await expect(page).toHaveURL(new RegExp(`/o/${slug}/infrastructure$`));
  await expect(page.getByTestId('asset-row')).toHaveCount(1);
  const gone = await page.request.post('/api/v1/agent/heartbeat', { headers: { Authorization: `Bearer ${token2}` }, data: {} });
  expect(gone.status()).toBe(401);
});

test('viewers see infrastructure read-only', async ({ page }) => {
  await signIn(page, 'viewer@demo.opshub.local', SEED_PASSWORD);
  await switchLanguage(page, 'en');
  await page.goto('/o/angkor-tech/infrastructure');
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Infrastructure');
  await expect(page.getByTestId('asset-row').filter({ hasText: 'web-1' })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Add asset' })).toHaveCount(0);
  await page.getByRole('link', { name: 'web-1' }).click();
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('web-1');
  await expect(page.getByText('Usage', { exact: true })).toBeVisible();
  for (const name of ['Edit', 'Delete', 'Rotate token', 'Create agent token']) {
    await expect(page.getByRole('button', { name })).toHaveCount(0);
  }
});
