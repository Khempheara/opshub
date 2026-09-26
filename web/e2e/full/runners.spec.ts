import { expect, test } from '@playwright/test';
import { expectKhmerLayoutOK } from '../khmer';
import { newOwnerWithOrg, switchLanguage } from './helpers';

test('register a runner, see it online, edit, disable and delete it', async ({ page }) => {
  test.setTimeout(90_000);
  const slug = await newOwnerWithOrg(page, 'Mekong Build Farm');
  await page.goto(`/o/${slug}/runners`);
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Runners');
  await expect(page.getByText('No runners yet')).toBeVisible();

  // Registration token: invalid labels are refused in the form, then shown once.
  await page.getByRole('button', { name: 'Register runner' }).click();
  const dialog = page.getByRole('dialog');
  await dialog.getByLabel('Labels').fill('linux, Bad Label!');
  await dialog.getByRole('button', { name: 'Create token' }).click();
  await expect(dialog.getByText('Use lowercase letters, numbers, dots, dashes and underscores')).toBeVisible();
  await dialog.getByLabel('Labels').fill('gpu');
  await dialog.getByRole('button', { name: 'Create token' }).click();
  const token = (await page.getByTestId('registration-token').textContent()) ?? '';
  expect(token).toMatch(/^ohr_reg_/);
  await expect(page.getByRole('dialog')).toContainText(`--token ${token}`);
  await page.getByRole('button', { name: 'Done' }).click();

  // The agent's side: register with the token and send a heartbeat.
  const reg = await page.request.post('/api/v1/runner/register', {
    data: { token, name: 'mekong-1', labels: ['linux'], version: '1.0.0', os: 'linux', arch: 'amd64', max_concurrency: 2 },
  });
  expect(reg.status()).toBe(201);
  const { token: runnerToken } = (await reg.json()) as { token: string };
  const again = await page.request.post('/api/v1/runner/register', { data: { token, name: 'second' } });
  expect(again.status()).toBe(401);
  const hb = await page.request.post('/api/v1/runner/heartbeat', {
    headers: { Authorization: `Bearer ${runnerToken}` },
    data: { version: '1.0.1', os: 'linux', arch: 'amd64', running_job_ids: [] },
  });
  expect(hb.status()).toBe(200);

  await page.reload();
  const row = page.getByTestId('runner-row');
  await expect(row).toContainText('mekong-1');
  await expect(row).toContainText('Online');
  await expect(row).toContainText('gpu');
  await expect(row).toContainText('0 of 2 jobs');
  await expect(row).toContainText('1.0.1 · linux/amd64');

  await page.getByRole('button', { name: 'Edit mekong-1' }).click();
  const edit = page.getByRole('dialog');
  await edit.getByLabel('Name').fill('mekong-big');
  await edit.getByLabel('Jobs at once').fill('65');
  await edit.getByRole('button', { name: 'Save' }).click();
  await expect(edit.getByText('Enter a whole number from 1 to 64.')).toBeVisible();
  await edit.getByLabel('Jobs at once').fill('4');
  await edit.getByRole('button', { name: 'Save' }).click();
  await expect(row).toContainText('mekong-big');
  await expect(row).toContainText('0 of 4 jobs');

  await row.getByRole('button', { name: 'Disable' }).click();
  await page.getByRole('alertdialog').getByRole('button', { name: 'Disable' }).click();
  await expect(row).toContainText('Disabled');
  const refused = await page.request.post('/api/v1/runner/heartbeat', {
    headers: { Authorization: `Bearer ${runnerToken}` },
    data: {},
  });
  expect(refused.status()).toBe(403);
  await row.getByRole('button', { name: 'Enable' }).click();
  await expect(row).not.toContainText('Disabled');

  // Khmer layout on phone and desktop widths.
  await switchLanguage(page, 'km');
  for (const viewport of [
    { width: 375, height: 812 },
    { width: 1280, height: 800 },
  ]) {
    await page.setViewportSize(viewport);
    await page.goto(`/o/${slug}/runners`);
    await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
    await expectKhmerLayoutOK(page);
  }
  await page.getByRole('button', { name: 'EN' }).click();

  await page.getByRole('button', { name: 'Delete mekong-big' }).click();
  await page.getByRole('alertdialog').getByRole('button', { name: 'Delete' }).click();
  await expect(page.getByText('No runners yet')).toBeVisible();
  const gone = await page.request.post('/api/v1/runner/heartbeat', { headers: { Authorization: `Bearer ${runnerToken}` }, data: {} });
  expect(gone.status()).toBe(401);
});
