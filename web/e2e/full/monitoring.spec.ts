import { expect, test, type Page } from '@playwright/test';
import { expectKhmerLayoutOK } from '../khmer';
import { MAILPIT, newOwnerWithOrg, SEED_PASSWORD, signIn, switchLanguage, uniqueEmail } from './helpers';

/** Opens a Monitoring tab and waits until it is shown (a closing dialog can swallow a click). */
async function openTab(page: Page, name: string, path: string) {
  await expect(async () => {
    await page.getByRole('navigation', { name: 'Monitoring sections' }).getByRole('link', { name, exact: true }).click();
    await expect(page).toHaveURL(new RegExp(`/monitoring${path}$`), { timeout: 2000 });
  }).toPass({ timeout: 15_000 });
}

// Real checks run every 15 s and rules are evaluated every 30 s, so this waits on the
// workers. Targets are ".invalid" names, which never resolve: always down, no internet needed.
test('a down monitor fires an alert that is acknowledged and silenced; channels test', async ({ page }) => {
  test.setTimeout(180_000);
  const slug = await newOwnerWithOrg(page, 'Mekong Uptime');
  await page.goto(`/o/${slug}/monitoring`);
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Monitoring');
  await expect(page.getByText('No monitors yet')).toBeVisible();

  // A channel first, so the rule can notify it. A webhook that can't be reached reports why.
  await openTab(page, 'Channels', '/channels');
  await page.getByRole('button', { name: 'Add channel' }).click();
  let dialog = page.getByRole('dialog');
  await dialog.getByLabel('Name').fill('ops-hook');
  await dialog.getByLabel('Type').selectOption('webhook');
  await dialog.getByLabel('URL').fill('ftp://nope');
  await dialog.getByRole('button', { name: 'Create' }).click();
  await expect(dialog.getByText('Enter a valid URL.')).toBeVisible();
  await dialog.getByLabel('URL').fill('https://hooks.opshub.invalid/alerts');
  await dialog.getByRole('button', { name: 'Create' }).click();
  const hookRow = page.getByTestId('channel-row').filter({ hasText: 'ops-hook' });
  await expect(hookRow).toBeVisible();
  await hookRow.getByRole('button', { name: 'Send test' }).click();
  await expect(page.getByText('Test failed: connection failed: host not found')).toBeVisible();
  await expect(hookRow).toContainText('failed');

  // An email channel delivers a test message (Mailpit).
  const inbox = uniqueEmail('oncall');
  await page.getByRole('button', { name: 'Add channel' }).click();
  dialog = page.getByRole('dialog');
  await dialog.getByLabel('Name').fill('on-call mail');
  await dialog.getByLabel('Type').selectOption('email');
  await dialog.getByLabel('Email addresses').fill(inbox);
  await dialog.getByRole('button', { name: 'Create' }).click();
  const mailRow = page.getByTestId('channel-row').filter({ hasText: 'on-call mail' });
  await mailRow.getByRole('button', { name: 'Send test' }).click();
  await expect(page.getByText('Test message sent')).toBeVisible();
  await expect
    .poll(async () => {
      const res = await page.request.get(`${MAILPIT}/api/v1/search`, { params: { query: `to:"${inbox}"` } });
      const { messages } = (await res.json()) as { messages: { Subject: string }[] };
      return messages[0]?.Subject ?? '';
    }, { timeout: 20_000 })
    .toBe('Test message from OpsHub');

  // A monitor that is always down.
  await openTab(page, 'Monitors', '');
  await page.getByRole('button', { name: 'Add monitor' }).click();
  dialog = page.getByRole('dialog');
  await dialog.getByLabel('Name').fill('payments-db');
  await dialog.getByLabel('Type').selectOption('tcp');
  await dialog.getByLabel('Target').fill('payments-db');
  await dialog.getByRole('button', { name: 'Create' }).click();
  await expect(dialog.getByText('Enter a host and port, e.g. db.internal:5432.')).toBeVisible();
  await dialog.getByLabel('Target').fill('payments-db.opshub.invalid:5432');
  await dialog.getByLabel('Labels').fill('prod, db');
  await dialog.getByRole('button', { name: 'Create' }).click();
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('payments-db');
  const monitorUrl = page.url();
  await expect(page.getByText('connection failed: host not found').first()).toBeVisible({ timeout: 45_000 });
  await expect(page.locator('[data-status="down"]').first()).toBeVisible({ timeout: 20_000 });

  // A rule for prod monitors that fires at once and notifies the webhook.
  await page.getByRole('link', { name: 'All monitors' }).click();
  await openTab(page, 'Alert rules', '/rules');
  await page.getByRole('button', { name: 'Add rule' }).click();
  dialog = page.getByRole('dialog');
  await dialog.getByLabel('Name').fill('Prod down');
  await dialog.getByLabel('Only with label or tag').fill('prod');
  await dialog.getByLabel('For at least (minutes)').fill('0');
  await dialog.getByLabel('Severity').selectOption('critical');
  await dialog.getByRole('checkbox', { name: /ops-hook/ }).check();
  await dialog.getByRole('button', { name: 'Create' }).click();
  const ruleRow = page.getByTestId('rule-row').filter({ hasText: 'Prod down' });
  await expect(ruleRow).toContainText('1 step');

  // Within one evaluation (30 s) the alert fires.
  await openTab(page, 'Alerts', '/alerts');
  await expect(async () => {
    await page.reload();
    await expect(page.getByTestId('alert-row').first()).toContainText('Prod down', { timeout: 2000 });
  }).toPass({ timeout: 60_000 });
  const alertRow = page.getByTestId('alert-row').first();
  await expect(alertRow).toContainText('payments-db is down: connection failed: host not found');
  await expect(alertRow).toContainText('Firing');
  await alertRow.getByRole('link', { name: 'Prod down' }).click();
  const timeline = page.getByTestId('alert-timeline');
  await expect(timeline).toContainText('Started firing');
  // The webhook can't be reached: the failed attempt is on the timeline.
  await expect(async () => {
    await page.reload();
    await expect(page.getByTestId('alert-timeline')).toContainText("Couldn't send to ops-hook", { timeout: 2000 });
  }).toPass({ timeout: 45_000 });

  await page.getByRole('button', { name: 'Acknowledge' }).click();
  await expect(page.getByText('Alert acknowledged: no further escalation')).toBeVisible();
  await expect(timeline).toContainText('Acknowledged by Org Owner');
  await expect(page.getByRole('button', { name: 'Acknowledge' })).toHaveCount(0);

  // Silence it from the alert.
  await page.getByRole('button', { name: 'Silence' }).click();
  dialog = page.getByRole('dialog');
  await dialog.getByLabel('For').selectOption('4h');
  await dialog.getByLabel('Comment').fill('DB migration');
  await dialog.getByRole('button', { name: 'Create' }).click();
  await expect(page.getByText('Silence added')).toBeVisible();
  await page.getByRole('link', { name: 'All alerts' }).click();
  await expect(page.getByTestId('alert-row').first()).toContainText('Silenced');
  await openTab(page, 'Silences', '/silences');
  const silenceRow = page.getByTestId('silence-row').first();
  await expect(silenceRow).toContainText('rule Prod down');
  await expect(silenceRow).toContainText('DB migration');
  await expect(silenceRow).toContainText('Active');
  await silenceRow.getByRole('button', { name: 'End now' }).click();
  await page.getByRole('alertdialog').getByRole('button', { name: 'End now' }).click();
  await expect(page.getByText('Silence ended')).toBeVisible();
  await expect(page.getByText('No silences')).toBeVisible();

  // A silence must match something.
  await page.getByRole('button', { name: 'Add silence' }).click();
  dialog = page.getByRole('dialog');
  await dialog.getByRole('button', { name: 'Create' }).click();
  await expect(dialog.getByText('Choose a rule, a severity or a label')).toBeVisible();
  await dialog.getByRole('button', { name: 'Cancel' }).click();

  // The channel a rule uses can't be deleted.
  await openTab(page, 'Channels', '/channels');
  await hookRow.getByRole('button', { name: 'Delete ops-hook?' }).click();
  await page.getByRole('alertdialog').getByRole('button', { name: 'Delete' }).click();
  await expect(page.getByText('Alert rules still notify this channel')).toBeVisible();

  // Khmer layout on phone and desktop widths.
  await switchLanguage(page, 'km');
  for (const viewport of [
    { width: 375, height: 812 },
    { width: 1280, height: 800 },
  ]) {
    await page.setViewportSize(viewport);
    for (const path of ['', '/alerts', '/rules', '/silences', '/channels']) {
      await page.goto(`/o/${slug}/monitoring${path}`);
      await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
      await expectKhmerLayoutOK(page);
    }
    await page.goto(monitorUrl);
    await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
    await expectKhmerLayoutOK(page);
  }
  await switchLanguage(page, 'en');

  // Deleting the monitor keeps the alert history.
  await page.goto(monitorUrl);
  await page.getByRole('button', { name: 'Delete' }).click();
  await page.getByRole('alertdialog').getByRole('button', { name: 'Delete' }).click();
  await expect(page).toHaveURL(new RegExp(`/o/${slug}/monitoring$`));
  await expect(page.getByText('No monitors yet')).toBeVisible();
});

test("viewers see monitoring read-only and don't see channels", async ({ page }) => {
  await signIn(page, 'viewer@demo.opshub.local', SEED_PASSWORD);
  await switchLanguage(page, 'en');
  await page.goto('/o/angkor-tech/monitoring');
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Monitoring');
  await expect(page.getByTestId('monitor-row').first()).toBeVisible();
  await expect(page.getByRole('button', { name: 'Add monitor' })).toHaveCount(0);
  await expect(page.getByRole('link', { name: 'Channels' })).toHaveCount(0);
  await openTab(page, 'Alert rules', '/rules');
  await expect(page.getByTestId('rule-row').first()).toBeVisible();
  await expect(page.getByRole('button', { name: 'Add rule' })).toHaveCount(0);
  await page.goto('/o/angkor-tech/monitoring/channels');
  await expect(page.getByRole('heading', { level: 1 })).not.toHaveText('Monitoring');
});
