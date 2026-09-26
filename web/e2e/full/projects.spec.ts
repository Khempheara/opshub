import { expect, test, type Page } from '@playwright/test';
import { expectKhmerLayoutOK } from '../khmer';
import { demoProjectLink, newOwnerWithOrg, SEED_PASSWORD, signIn, switchLanguage } from './helpers';

test.describe.configure({ mode: 'serial' });

const tab = (page: Page, name: string) => page.getByRole('navigation', { name: 'Project sections' }).getByRole('link', { name });

test('create a project, a protected environment, then edit it', async ({ page }) => {
  test.setTimeout(60_000);
  const org = await newOwnerWithOrg(page, 'Siem Reap Software');
  await page.goto(`/o/${org}/projects`);
  await expect(page.getByText('No projects yet')).toBeVisible();

  await page.getByRole('button', { name: 'New project' }).click();
  const dialog = page.getByRole('dialog');
  await dialog.getByLabel('Project name').fill('Booking Service');
  await expect(dialog.getByLabel('URL name')).toHaveValue('booking-service');
  await dialog.getByRole('button', { name: 'Create project' }).click();
  await expect(page).toHaveURL(new RegExp(`/o/${org}/projects/[0-9a-f-]+$`));
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Booking Service');

  await tab(page, 'Environments').click();
  await page.getByRole('button', { name: 'New environment' }).click();
  const env = page.getByRole('dialog');
  await env.getByLabel('Name', { exact: true }).fill('production');
  await env.getByLabel('Type').selectOption('production');
  await env.getByRole('button', { name: 'Add variable' }).click();
  await env.getByRole('textbox', { name: 'Name' }).nth(1).fill('REGION');
  await env.getByRole('textbox', { name: 'Value' }).fill('ap-southeast-1');
  await env.getByLabel('Protect this environment').check();
  await env.getByLabel('Required approvals').fill('2');
  await env.getByLabel('Allowed branches').fill('main\nrelease/*');
  await env.getByRole('button', { name: 'New environment' }).click();

  const card = page.getByTestId('environment-production');
  await expect(card).toContainText('Protected');
  await expect(card).toContainText('1 variable');
  await expect(card).toContainText('2 approvals · main, release/* · Owner, Admin');

  // A duplicate name is reported on the field.
  await page.getByRole('button', { name: 'New environment' }).click();
  await page.getByRole('dialog').getByLabel('Name', { exact: true }).fill('production');
  await page.getByRole('dialog').getByRole('button', { name: 'New environment' }).click();
  await expect(page.getByRole('dialog').getByText('An environment with this name already exists.')).toBeVisible();
  await page.getByRole('dialog').getByRole('button', { name: 'Cancel' }).click();

  // Edit: remove protection.
  await card.getByRole('button', { name: 'Edit' }).click();
  await page.getByRole('dialog').getByLabel('Protect this environment').uncheck();
  await page.getByRole('dialog').getByRole('button', { name: 'Save' }).click();
  await expect(card).not.toContainText('Protected');

  // Settings: rename (If-Match) and see it in the header.
  await tab(page, 'Settings').click();
  await page.getByLabel('Project name').fill('Booking API');
  await page.getByRole('button', { name: 'Save' }).click();
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Booking API');
});

test('a self-hosted Git URL on an internal address is refused (SSRF)', async ({ page }) => {
  const org = await newOwnerWithOrg(page, 'Kep Cloud');
  await page.goto(`/o/${org}/projects`);
  await page.getByRole('button', { name: 'New project' }).click();
  await page.getByRole('dialog').getByLabel('Project name').fill('Crab API');
  await page.getByRole('dialog').getByRole('button', { name: 'Create project' }).click();
  await tab(page, 'Repository').click();

  await page.getByRole('textbox', { name: 'Repository', exact: true }).fill('acme/crab-api');
  await page.getByLabel('Access token').fill('not-a-real-token');
  await page.getByLabel('Self-hosted URL (optional)').fill('https://127.0.0.1:8443');
  await page.getByRole('button', { name: 'Connect repository' }).click();
  await expect(page.getByText("This address points to an internal network and isn't allowed.")).toBeVisible();
});

test('an org Viewer sees the demo project read-only', async ({ page }) => {
  await signIn(page, 'viewer@demo.opshub.local', SEED_PASSWORD);
  await page.goto('/o/angkor-tech/projects');
  await expect(page.getByRole('button', { name: 'New project' })).toHaveCount(0);
  await demoProjectLink(page).click();
  await tab(page, 'Settings').click();
  await expect(page.getByText('Only project Admins can change these settings.')).toBeVisible();
  await expect(page.getByLabel('Project name')).toBeDisabled();
  await expect(page.getByRole('button', { name: 'Delete project' })).toHaveCount(0);

  await tab(page, 'Environments').click();
  await expect(page.getByTestId('environment-production')).toContainText('Protected');
  await expect(page.getByRole('button', { name: 'New environment' })).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Edit' })).toHaveCount(0);

  await tab(page, 'Repository').click();
  await expect(page.getByText('A project Admin can connect a repository.')).toBeVisible();
  await tab(page, 'Access').click();
  await expect(page.getByTestId('project-access')).toContainText('owner@demo.opshub.local');
  await expect(page.getByRole('button', { name: 'Grant access' })).toHaveCount(0);
});

test('Khmer layout of the project pages on mobile and desktop', async ({ page }) => {
  await signIn(page, 'owner@demo.opshub.local', SEED_PASSWORD);
  await page.goto('/o/angkor-tech/projects');
  await switchLanguage(page, 'km');
  await expect(page.locator('html')).toHaveAttribute('lang', 'km');
  const project = (await demoProjectLink(page).getAttribute('href')) ?? '';
  const pages = ['/o/angkor-tech/projects', `${project}/settings`, `${project}/environments`, `${project}/repository`, `${project}/access`];
  for (const viewport of [
    { width: 375, height: 812 },
    { width: 1280, height: 800 },
  ]) {
    await page.setViewportSize(viewport);
    for (const path of pages) {
      await page.goto(path);
      await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
      await expectKhmerLayoutOK(page);
    }
  }
});
