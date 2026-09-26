import { expect, test } from '@playwright/test';
import { expectKhmerLayoutOK } from '../khmer';
import { newOwnerWithOrg, SEED_PASSWORD, signIn, switchLanguage } from './helpers';

test('the overview shows DORA metrics and pipeline statistics from the demo history', async ({ page }) => {
  test.setTimeout(90_000);
  await signIn(page, 'admin@demo.opshub.local', SEED_PASSWORD);
  await switchLanguage(page, 'en');
  await page.goto('/o/angkor-tech');
  await expect(page.getByRole('heading', { name: 'Delivery performance' })).toBeVisible();

  // The seeded checkout-web history gives every metric a value and a DORA level.
  for (const id of ['metric-frequency', 'metric-lead-time', 'metric-failure-rate', 'metric-restore']) {
    const card = page.getByTestId(id);
    await expect(card.getByTestId(`${id}-value`)).toBeVisible();
    await expect(card.locator('[data-level]')).toHaveText(/^(Elite|High|Medium|Low)$/);
  }
  await expect(page.getByTestId('metric-frequency-value')).toHaveText(/per (day|week|month)$/);
  await expect(page.getByTestId('metric-failure-rate-value')).toHaveText(/%$/);
  await expect(page.getByTestId('metric-runs-value')).not.toHaveText('0');
  await expect(page.getByRole('img', { name: /^Production changes: / })).toBeVisible();
  await expect(page.getByRole('img', { name: /^Runs: / })).toBeVisible();
  const rows = page.getByTestId('dashboard-project-row');
  await expect(rows.first()).toContainText('Checkout Web');

  // One project, a shorter range.
  await page.getByLabel('Project', { exact: true }).selectOption({ label: 'Checkout Web · ទំព័រទូទាត់' });
  await expect(rows).toHaveCount(1);
  await page.getByLabel('Time range').selectOption('7d');
  await expect(rows).toHaveCount(1);
  await page.getByLabel('Time range').selectOption('365d');
  await expect(page.getByTestId('metric-lead-time-value')).toBeVisible();
  await rows.first().getByRole('link', { name: /Checkout Web/ }).click();
  await expect(page).toHaveURL(/\/o\/angkor-tech\/projects\/[0-9a-f-]+$/);
});

test('a new organization sees getting started and an empty dashboard, in Khmer too', async ({ page }) => {
  test.setTimeout(90_000);
  const slug = await newOwnerWithOrg(page, 'Kep Crab Deploys');
  await expect(page.getByTestId('getting-started')).toContainText('Create your first project');
  await expect(page.getByTestId('metric-frequency')).toContainText('No production deployments');
  await expect(page.getByTestId('metric-lead-time')).toContainText('No pipeline deployments to production yet');
  await expect(page.getByTestId('metric-restore')).toContainText('No failed changes');
  await expect(page.getByText('Nothing happened in this period')).toBeVisible();

  await switchLanguage(page, 'km');
  for (const viewport of [
    { width: 375, height: 812 },
    { width: 1280, height: 800 },
  ]) {
    await page.setViewportSize(viewport);
    await page.goto(`/o/${slug}`);
    await expect(page.getByRole('heading', { name: 'សមិទ្ធផលនៃការបញ្ជូន' })).toBeVisible();
    await expectKhmerLayoutOK(page);
  }
  await switchLanguage(page, 'en');
});

test('the dashboard lays out in Khmer with data at phone and desktop widths', async ({ page }) => {
  // viewer@ is English by default; switch and switch back within this test.
  await signIn(page, 'viewer@demo.opshub.local', SEED_PASSWORD);
  await switchLanguage(page, 'km');
  for (const viewport of [
    { width: 375, height: 812 },
    { width: 1280, height: 800 },
  ]) {
    await page.setViewportSize(viewport);
    await page.goto('/o/angkor-tech');
    await expect(page.getByTestId('metric-frequency-value')).toBeVisible();
    await expectKhmerLayoutOK(page);
  }
  await switchLanguage(page, 'en');
});
