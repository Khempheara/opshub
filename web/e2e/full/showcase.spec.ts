import { expect, test } from '@playwright/test';
import { SEED_PASSWORD, signIn } from './helpers';

// The showcase organization (make seed) fills every page; sign-in still opens angkor-tech.
test('the showcase organization is one switch away and shows its seeded states', async ({ page }) => {
  await signIn(page, 'admin@demo.opshub.local', SEED_PASSWORD);
  await expect(page).toHaveURL(/\/o\/angkor-tech$/);

  await page.getByRole('button', { name: 'Switch organization' }).click();
  await page.getByRole('menuitem', { name: /Mekong Cloud/ }).click();
  await expect(page).toHaveURL(/\/o\/mekong-cloud$/);

  await page.goto('/o/mekong-cloud/monitoring/alerts');
  const rows = page.getByTestId('alert-row');
  await expect(rows.filter({ hasText: 'api-2 agent offline' }).first()).toContainText('Acknowledged');
  await expect(rows.filter({ hasText: 'old.mekong.example' })).toContainText('Silenced');

  await page.goto('/o/mekong-cloud/monitoring/rules');
  await expect(page.getByTestId('rule-row')).toHaveCount(9);

  await page.goto('/o/mekong-cloud/infrastructure/certificates');
  await expect(page.getByText('Expiring soon')).toBeVisible();
});
