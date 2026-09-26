import { readFile } from 'node:fs/promises';
import { expect, test } from '@playwright/test';
import { expectKhmerLayoutOK } from '../khmer';
import { newOwnerWithOrg, SEED_PASSWORD, signIn, switchLanguage } from './helpers';

test('owners read the audit log as sentences, filter it and export it as CSV', async ({ page }) => {
  test.setTimeout(90_000);
  const slug = await newOwnerWithOrg(page, 'Siem Reap Audit');

  // Something to audit: a team with a member.
  await page.goto(`/o/${slug}/teams`);
  await page.getByRole('button', { name: 'New team' }).click();
  await page.getByRole('dialog').getByLabel('Team name').fill('Release crew');
  await page.getByRole('dialog').getByRole('button', { name: 'New team' }).click();
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Release crew');
  await page.getByLabel('Choose a member').selectOption({ index: 1 });
  await page.getByRole('button', { name: 'Add member' }).click();
  await expect(page.getByTestId('team-members').locator('li')).toHaveCount(1);

  await page.getByRole('link', { name: 'Audit log' }).click();
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Audit log');
  const summaries = page.getByTestId('audit-summary');
  await expect(summaries).toHaveText([
    /^Org Owner added .+ to the team Release crew$/,
    'Org Owner created the team Release crew',
    'Org Owner created the organization Siem Reap Audit',
  ]);

  // Details show the action and what changed.
  const created = page.getByTestId('audit-entry').filter({ hasText: 'created the team' });
  await created.getByRole('button').click();
  const details = created.getByTestId('audit-details');
  await expect(details).toContainText('team.create');
  await expect(details).toContainText('Release crew');
  await expect(details).toContainText('Person · Org Owner');

  // Filters.
  await page.getByLabel('Area').selectOption('projects');
  await expect(page.getByText('No entries for these filters')).toBeVisible();
  await page.getByLabel('Area').selectOption('organization');
  await expect(summaries).toHaveCount(3);
  await page.getByLabel('Time range').selectOption('all');
  await expect(summaries).toHaveCount(3);

  // CSV export: UTF-8 with a BOM, and the export is itself recorded.
  const [download] = await Promise.all([page.waitForEvent('download'), page.getByRole('button', { name: 'Export CSV' }).click()]);
  expect(download.suggestedFilename()).toMatch(new RegExp(`^audit-log-${slug}-\\d{8}\\.csv$`));
  const csv = await readFile(await download.path());
  expect([...csv.subarray(0, 3)]).toEqual([0xef, 0xbb, 0xbf]);
  const text = csv.subarray(3).toString('utf8');
  expect(text.split('\n')[0]).toBe('time,actor_type,actor_name,actor_email,action,summary,resource_type,resource_id,project_id,project_name,ip,user_agent,before,after,metadata');
  expect(text).toContain('Org Owner created the team Release crew');
  await page.getByLabel('Area').selectOption('');
  await expect(summaries.first()).toHaveText('Org Owner exported the audit log');

  // Your own account activity lives in Settings → Security.
  await page.goto('/settings/security');
  const activity = page.getByTestId('account-activity');
  await expect(activity.getByTestId('audit-summary').first()).toHaveText('Org Owner signed in');
  await expect(activity).toContainText('Org Owner confirmed their email address');
  await expect(activity).not.toContainText('Release crew');

  // Khmer: sentences and layout at phone and desktop widths.
  await switchLanguage(page, 'km');
  for (const viewport of [
    { width: 375, height: 812 },
    { width: 1280, height: 800 },
  ]) {
    await page.setViewportSize(viewport);
    await page.goto(`/o/${slug}/audit-log`);
    await expect(page.getByTestId('audit-summary').first()).toHaveText('Org Owner បាននាំចេញកំណត់ត្រាសវនកម្ម');
    await page.getByTestId('audit-entry').filter({ hasText: 'បានបង្កើតក្រុម' }).getByRole('button').click();
    await expect(page.getByTestId('audit-details')).toBeVisible();
    await expectKhmerLayoutOK(page);
    await page.goto('/settings/security');
    await expect(page.getByTestId('account-activity').getByTestId('audit-summary').first()).toHaveText(/បាន/);
    await expectKhmerLayoutOK(page);
  }
  await switchLanguage(page, 'en');
});

test("developers don't see the audit log", async ({ page }) => {
  // dev@ is the Khmer demo user: never switch its language here.
  await signIn(page, 'dev@demo.opshub.local', SEED_PASSWORD);
  await page.goto('/o/angkor-tech');
  await expect(page.locator('a[href="/o/angkor-tech/logs"]').first()).toBeVisible();
  await expect(page.locator('a[href="/o/angkor-tech/audit-log"]')).toHaveCount(0);
  await page.goto('/o/angkor-tech/audit-log');
  await expect(page.getByTestId('audit-entries')).toHaveCount(0);
  await expect(page.getByRole('heading', { level: 1 })).not.toHaveText('កំណត់ត្រាសវនកម្ម');
  // Their own account activity is still theirs to see.
  await page.goto('/settings/security');
  await expect(page.getByTestId('account-activity').getByTestId('audit-summary').first()).toContainText('បានចូលគណនី');
});
