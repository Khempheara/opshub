import { expect, test, type Page } from '@playwright/test';
import { expectKhmerLayoutOK } from '../khmer';
import { newOwnerWithOrg, SEED_PASSWORD, signIn, switchLanguage } from './helpers';

// Runs against the demo data from `make seed`: the demo-k8s target (a cluster that doesn't
// resolve, so every deployment started here fails safely as "target unreachable") and a
// history where production runs 1.4.0 after 1.3.0.
test.describe.configure({ mode: 'serial' });

async function openDeployments(page: Page) {
  await page.goto('/o/angkor-tech/projects');
  await page.getByTestId('project-list').getByRole('link').first().click();
  await page.getByRole('link', { name: 'Deployments' }).click();
  await expect(page.getByTestId('deployments-table')).toBeVisible();
}

test('release history, a manual deploy and a protected environment', async ({ page }) => {
  test.setTimeout(60_000);
  await signIn(page, 'admin@demo.opshub.local', SEED_PASSWORD);
  await openDeployments(page);
  const live = page.getByTestId('current-releases');
  await expect(live).toContainText('production');
  await expect(live).toContainText('payments-api:1.4.0');
  await expect(page.getByTestId('deployments-table')).toContainText('reverted');

  // Filters.
  await page.getByLabel('Status').selectOption('failed');
  for (const row of await page.getByTestId('deployment-row').all()) await expect(row).toContainText('Failed');
  await page.getByLabel('Status').selectOption('');

  // An invalid image is refused on the field; a valid one starts and fails safely.
  await page.getByRole('button', { name: 'Deploy' }).click();
  const dialog = page.getByRole('dialog');
  await dialog.getByLabel('Environment').selectOption({ label: 'staging' });
  await dialog.getByLabel('Target').selectOption({ label: 'demo-k8s (Kubernetes)' });
  await dialog.getByLabel('Image').fill('Payments API:latest');
  await dialog.getByRole('button', { name: 'Deploy' }).click();
  await expect(dialog.getByText('Enter an image reference')).toBeVisible();
  await dialog.getByLabel('Image').fill('ghcr.io/angkor-tech/payments-api:1.5.0-rc1');
  await dialog.getByLabel('Strategy').selectOption('blue_green');
  await dialog.getByRole('button', { name: 'Deploy' }).click();
  await expect(page.getByRole('heading', { level: 2, name: /Deployment #\d+/ })).toBeVisible();
  await expect(page.getByTestId('deployment-status')).toContainText('Failed', { timeout: 30_000 });
  await expect(page.getByText("OpsHub couldn't reach the target.")).toBeVisible();
  await expect(page.getByTestId('job-log')).toContainText('Deploying ghcr.io/angkor-tech/payments-api:1.5.0-rc1 to staging');

  // Production requires approvals: manual deploys are refused with a clear message.
  await page.getByRole('link', { name: 'All deployments' }).click();
  await page.getByRole('button', { name: 'Deploy' }).click();
  await dialog.getByLabel('Environment').selectOption({ label: 'production · protected' });
  await dialog.getByLabel('Target').selectOption({ label: 'demo-k8s (Kubernetes)' });
  await dialog.getByLabel('Image').fill('ghcr.io/angkor-tech/payments-api:1.5.0');
  await dialog.getByRole('button', { name: 'Deploy' }).click();
  await expect(dialog.getByRole('alert')).toContainText("This environment is protected: you can't deploy to it from here.");
});

test('one-click rollback of the current production release', async ({ page }) => {
  test.setTimeout(60_000);
  await signIn(page, 'admin@demo.opshub.local', SEED_PASSWORD);
  await openDeployments(page);
  await page.getByTestId('current-releases').getByRole('link', { name: /payments-api:1\.4\.0/ }).click();
  await expect(page.getByTestId('deployment-status')).toContainText('Live');
  await page.getByRole('button', { name: 'Roll back' }).click();
  const confirm = page.getByRole('alertdialog');
  await expect(confirm).toContainText('payments-api:1.4.0 is replaced by the previous release, ghcr.io/angkor-tech/payments-api:1.3.0');
  await confirm.getByRole('button', { name: 'Roll back' }).click();
  await expect(page.getByRole('heading', { level: 2, name: /Rollback #\d+/ })).toBeVisible();
  await expect(page.getByTestId('job-log')).toContainText('Rolling back production to ghcr.io/angkor-tech/payments-api:1.3.0');
  // The demo cluster can't be reached, so the rollback fails and 1.4.0 stays live.
  await expect(page.getByTestId('deployment-status')).toContainText('Failed', { timeout: 30_000 });
  await page.getByRole('link', { name: 'All deployments' }).click();
  await expect(page.getByTestId('current-releases')).toContainText('payments-api:1.4.0');
});

test('viewers see releases but cannot deploy or roll back', async ({ page }) => {
  await signIn(page, 'viewer@demo.opshub.local', SEED_PASSWORD);
  await openDeployments(page);
  await expect(page.getByRole('button', { name: 'Deploy' })).toHaveCount(0);
  await page.getByTestId('current-releases').getByRole('link', { name: /payments-api:1\.4\.0/ }).click();
  await expect(page.getByTestId('deployment-status')).toContainText('Succeeded');
  await expect(page.getByRole('button', { name: 'Roll back' })).toHaveCount(0);
  await page.goto('/o/angkor-tech/deploy-targets');
  await expect(page.getByTestId('targets-table')).toContainText('demo-k8s');
  await expect(page.getByRole('button', { name: 'Add target' })).toHaveCount(0);
});

test('manage deploy targets: validation, connection test, edit, delete', async ({ page }) => {
  test.setTimeout(60_000);
  const slug = await newOwnerWithOrg(page, 'Siem Reap Hosting');
  await page.goto(`/o/${slug}/deploy-targets`);
  await expect(page.getByText('No deploy targets yet')).toBeVisible();

  await page.getByRole('button', { name: 'Add target' }).click();
  const dialog = page.getByRole('dialog');
  await dialog.getByLabel('Name').fill('web');
  await dialog.getByLabel('Hosts', { exact: true }).fill('bad host name');
  await dialog.getByLabel('Deploy command').fill('docker compose pull && docker compose up -d');
  await dialog.getByLabel('Private key').fill('not a key');
  await dialog.getByRole('button', { name: 'Create' }).click();
  await expect(dialog.getByText('Enter a host name or address')).toBeVisible();
  await expect(dialog.getByText("This isn't a valid private key")).toBeVisible();

  // A syntactically valid target on a documentation address (nothing answers there).
  await dialog.getByLabel('Hosts', { exact: true }).fill('192.0.2.10');
  await dialog.getByLabel('Private key').fill('');
  await dialog.getByLabel('Sign in with').selectOption('password');
  await dialog.getByLabel('Password', { exact: true }).fill('correct-horse-battery');
  await dialog.getByRole('button', { name: 'Create' }).click();
  const row = page.getByTestId('target-row');
  await expect(row).toContainText('web');
  await expect(row).toContainText('192.0.2.10:22');
  await expect(row).toContainText('Not tested');

  await row.getByRole('button', { name: 'Test' }).click();
  const testDialog = page.getByRole('dialog');
  await testDialog.getByRole('button', { name: 'Test connection' }).click();
  await expect(testDialog.getByTestId('test-result')).toBeVisible({ timeout: 30_000 });
  await expect(testDialog.getByText('Fix the target')).toBeVisible();
  await testDialog.getByRole('button', { name: 'Close' }).first().click();
  await expect(row).toContainText('Test failed');

  // Editing keeps the stored password (only its field name is shown).
  await page.getByRole('button', { name: 'Edit web' }).click();
  await expect(dialog.getByText('Stored:')).toBeVisible();
  await expect(dialog.getByText('password', { exact: true })).toBeVisible();
  await dialog.getByLabel('Description').fill('Hetzner web nodes');
  await dialog.getByRole('button', { name: 'Save' }).click();
  await expect(row).toContainText('Hetzner web nodes');

  await page.getByRole('button', { name: 'Delete web' }).click();
  await page.getByRole('alertdialog').getByRole('button', { name: 'Delete' }).click();
  await expect(page.getByText('No deploy targets yet')).toBeVisible();
});

test('Khmer layout of the deployment pages on mobile and desktop', async ({ page }) => {
  await signIn(page, 'owner@demo.opshub.local', SEED_PASSWORD);
  await page.goto('/o/angkor-tech/projects');
  await switchLanguage(page, 'km');
  await expect(page.locator('html')).toHaveAttribute('lang', 'km');
  const project = (await page.getByTestId('project-list').getByRole('link').first().getAttribute('href')) ?? '';
  await page.goto(`${project}/deployments`);
  const detail = (await page.getByTestId('deployment-row').first().getByRole('link').first().getAttribute('href')) ?? '';
  for (const viewport of [
    { width: 375, height: 812 },
    { width: 1280, height: 800 },
  ]) {
    await page.setViewportSize(viewport);
    for (const path of [`${project}/deployments`, detail, '/o/angkor-tech/deploy-targets']) {
      await page.goto(path);
      await expect(page.locator('main')).toBeVisible();
      await expectKhmerLayoutOK(page);
    }
  }
});
