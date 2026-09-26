import { randomUUID } from 'node:crypto';
import { expect, test, type Page } from '@playwright/test';
import { expectKhmerLayoutOK } from '../khmer';
import { SEED_PASSWORD, signIn, switchLanguage } from './helpers';

// Runs against the demo project from `make seed`: SENTRY_DSN for all environments and
// DATABASE_URL for staging and (protected) production. Each run adds uniquely named secrets
// and deletes them again.
test.describe.configure({ mode: 'serial' });

async function openSecrets(page: Page, tab = 'Secrets') {
  await page.goto('/o/angkor-tech/projects');
  await page.getByTestId('project-list').getByRole('link').first().click();
  await page.getByRole('link', { name: tab, exact: true }).click();
  await expect(page.getByTestId('secrets-table')).toBeVisible();
}

const row = (page: Page, name: string, scope: string) =>
  page.getByTestId('secret-row').filter({ hasText: name }).filter({ hasText: scope });

test('create, rotate, edit and delete a secret; values are never shown', async ({ page }) => {
  test.setTimeout(60_000);
  const name = `E2E_${randomUUID().slice(0, 8).toUpperCase()}`;
  const value = `sk_e2e_${randomUUID()}`;
  await signIn(page, 'admin@demo.opshub.local', SEED_PASSWORD);
  await switchLanguage(page, 'en');
  await openSecrets(page);
  await expect(row(page, 'SENTRY_DSN', 'All environments')).toBeVisible();
  await expect(row(page, 'DATABASE_URL', 'production')).toBeVisible();

  // Names are normalized while typing; reserved names are refused.
  await page.getByRole('button', { name: 'Add secret' }).click();
  const dialog = page.getByRole('dialog');
  await dialog.getByLabel('Name').fill('opshub token');
  await expect(dialog.getByLabel('Name')).toHaveValue('OPSHUB_TOKEN');
  await dialog.getByRole('button', { name: 'Create' }).click();
  await expect(dialog.getByText('Names starting with OPSHUB_ are reserved.')).toBeVisible();
  await expect(dialog.getByText('This field is required.')).toBeVisible();

  await dialog.getByLabel('Name').fill(name);
  await dialog.getByLabel('Scope').selectOption({ label: 'staging' });
  await dialog.getByLabel('Description').fill('Created by E2E');
  await dialog.getByLabel('Value', { exact: true }).fill(value);
  await dialog.getByRole('button', { name: 'Create' }).click();
  await expect(dialog).toBeHidden();
  const mine = row(page, name, 'staging');
  await expect(mine).toContainText('v1');
  await expect(mine).toContainText('Created by E2E');
  await expect(page.locator('body')).not.toContainText(value);

  // The same name again in the same scope is refused.
  await page.getByRole('button', { name: 'Add secret' }).click();
  await dialog.getByLabel('Name').fill(name);
  await dialog.getByLabel('Scope').selectOption({ label: 'staging' });
  await dialog.getByLabel('Value', { exact: true }).fill('x');
  await dialog.getByRole('button', { name: 'Create' }).click();
  await expect(dialog.getByRole('alert')).toContainText('already exists');
  await dialog.getByRole('button', { name: 'Cancel' }).click();

  // Rotate with a multi-line value.
  await mine.getByRole('button', { name: `Rotate ${name}` }).click();
  await dialog.getByLabel('Multi-line value').check();
  await dialog.getByLabel('New value').fill(`${value}\nsecond-line`);
  await dialog.getByRole('button', { name: 'Rotate' }).click();
  await expect(mine).toContainText('v2');

  // History: who and when, the old value destroyed, and how to use it.
  await mine.getByRole('button', { name: `History of ${name}` }).click();
  const versions = dialog.getByTestId('secret-versions');
  await expect(versions.getByRole('listitem')).toHaveCount(2);
  await expect(versions.getByRole('listitem').first()).toContainText('current');
  await expect(versions.getByRole('listitem').last()).toContainText('value destroyed');
  await expect(dialog).toContainText(`secrets: [${name}]`);
  await expect(dialog).not.toContainText(value);
  await dialog.getByRole('button', { name: 'Close' }).first().click();

  await mine.getByRole('button', { name: `Edit ${name}` }).click();
  await dialog.getByLabel('Description').fill('Updated by E2E');
  await dialog.getByRole('button', { name: 'Save' }).click();
  await expect(mine).toContainText('Updated by E2E');

  // Filter by scope.
  await page.getByLabel('Show').selectOption({ label: 'All environments' });
  await expect(page.getByTestId('secret-row')).toHaveCount(1);
  await expect(page.getByTestId('secret-row')).toContainText('SENTRY_DSN');
  await page.getByLabel('Show').selectOption({ label: 'All secrets' });

  await mine.getByRole('button', { name: `Delete ${name}` }).click();
  await page.getByRole('alertdialog').getByRole('button', { name: 'Delete' }).click();
  await expect(mine).toHaveCount(0);
});

// dev@ is the seeded Khmer user; auth.spec.ts relies on that, so this test uses the Khmer UI
// and never changes the saved language.
test('developers manage only unprotected environments (Khmer UI)', async ({ page }) => {
  test.setTimeout(60_000);
  const name = `E2E_DEV_${randomUUID().slice(0, 6).toUpperCase()}`;
  await signIn(page, 'dev@demo.opshub.local', SEED_PASSWORD);
  await expect(page.locator('html')).toHaveAttribute('lang', 'km');
  await openSecrets(page, 'Secret');

  // Protected scopes are listed without controls.
  for (const [n, scope] of [
    ['SENTRY_DSN', 'គ្រប់បរិស្ថាន'],
    ['DATABASE_URL', 'production'],
  ]) {
    const r = row(page, n, scope);
    await expect(r.getByRole('button', { name: `ប្រវត្តិរបស់ ${n}` })).toBeVisible();
    await expect(r.getByRole('button', { name: `ប្ដូរ ${n}` })).toHaveCount(0);
  }
  await expect(row(page, 'DATABASE_URL', 'staging').getByRole('button', { name: 'ប្ដូរ DATABASE_URL' })).toBeVisible();

  // Only unprotected environments can be chosen.
  await page.getByRole('button', { name: 'បន្ថែម Secret' }).click();
  const dialog = page.getByRole('dialog');
  const options = await dialog.getByLabel('វិសាលភាព').locator('option').allTextContents();
  expect(options).not.toContain('គ្រប់បរិស្ថាន');
  expect(options.some((o) => o.includes('production'))).toBe(false);
  await dialog.getByLabel('ឈ្មោះ').fill(name);
  await dialog.getByLabel('វិសាលភាព').selectOption({ label: 'staging' });
  await dialog.getByLabel('តម្លៃ', { exact: true }).fill('dev-value');
  await expectKhmerLayoutOK(page);
  await dialog.getByRole('button', { name: 'បង្កើត' }).click();
  const mine = row(page, name, 'staging');
  await expect(mine).toBeVisible();

  // Khmer layout on phone and desktop widths.
  for (const viewport of [
    { width: 375, height: 812 },
    { width: 1280, height: 800 },
  ]) {
    await page.setViewportSize(viewport);
    await page.reload();
    await expect(page.getByTestId('secrets-table')).toBeVisible();
    await expectKhmerLayoutOK(page);
  }

  await mine.getByRole('button', { name: `លុប ${name}` }).click();
  await page.getByRole('alertdialog').getByRole('button', { name: 'លុប' }).click();
  await expect(mine).toHaveCount(0);
});

test("viewers don't get the Secrets tab", async ({ page }) => {
  await signIn(page, 'viewer@demo.opshub.local', SEED_PASSWORD);
  await switchLanguage(page, 'en');
  await page.goto('/o/angkor-tech/projects');
  await page.getByTestId('project-list').getByRole('link').first().click();
  await expect(page.getByRole('link', { name: 'Deployments', exact: true })).toBeVisible();
  await expect(page.getByRole('link', { name: 'Secrets', exact: true })).toHaveCount(0);
});
