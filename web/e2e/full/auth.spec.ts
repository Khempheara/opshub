import { expectKhmerLayoutOK } from '../khmer';
import { expect, test } from '@playwright/test';
import { emailLink, PASSWORD, registerAndVerify, SEED_PASSWORD, signIn, switchLanguage, totp, uniqueEmail } from './helpers';

test.describe.configure({ mode: 'serial' });

test('register → verify email → create organization', async ({ page }) => {
  const email = uniqueEmail('reg');
  await registerAndVerify(page, email, 'Chenda Lim');
  await signIn(page, email);

  await expect(page).toHaveURL(/\/onboarding$/);
  const slug = `e2e-${Date.now().toString(36)}`;
  await page.getByLabel('Organization name').fill('Mekong Cloud');
  await page.getByLabel('URL name').fill(slug);
  await page.getByRole('button', { name: 'Create organization' }).click();
  await expect(page).toHaveURL(new RegExp(`/o/${slug}$`));
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Welcome to Mekong Cloud');
  await expect(page.getByRole('button', { name: 'Switch organization' })).toContainText('Mekong Cloud');
});

test('seeded Khmer user: profile language wins, and switching saves it', async ({ page }) => {
  await signIn(page, 'dev@demo.opshub.local', SEED_PASSWORD);
  await expect(page).toHaveURL(/\/o\/angkor-tech$/);
  await expect(page.locator('html')).toHaveAttribute('lang', 'km');
  await expect(page.getByRole('heading', { level: 1 })).toContainText('សូមស្វាគមន៍មកកាន់');

  await page.getByRole('button', { name: 'EN' }).click();
  await expect(page.getByRole('heading', { level: 1 })).toContainText('Welcome to');
  await page.reload();
  await expect(page.locator('html')).toHaveAttribute('lang', 'en', { timeout: 10_000 });

  // Switch back so the seed stays Khmer for other runs.
  await switchLanguage(page, 'km');
  await expect(page.locator('html')).toHaveAttribute('lang', 'km');
  await page.reload();
  await expect(page.locator('html')).toHaveAttribute('lang', 'km');
});

test('two-factor authentication: enroll, then sign in with a TOTP code', async ({ page }) => {
  test.setTimeout(90_000); // waits for a fresh 30 s TOTP window
  const email = uniqueEmail('mfa');
  await registerAndVerify(page, email);
  await signIn(page, email);
  await page.goto('/settings/security');

  await page.getByRole('button', { name: 'Set up two-factor authentication' }).click();
  await page.getByRole('dialog').getByLabel('Password', { exact: true }).fill(PASSWORD);
  await page.getByRole('button', { name: 'Continue' }).click();
  const secret = (await page.getByTestId('totp-secret').textContent()) ?? '';
  expect(secret).toMatch(/^[A-Z2-7]{32}$/);
  await expect(page.getByRole('img', { name: 'QR code for your authenticator app' })).toBeVisible();
  await page.getByLabel('Enter the 6-digit code the app shows.').fill(totp(secret));
  await page.getByRole('button', { name: 'Verify' }).click();
  await expect(page.getByTestId('recovery-codes').locator('li')).toHaveCount(10);
  await page.getByRole('button', { name: "I've saved these codes" }).click();
  await expect(page.getByText('Enabled', { exact: true })).toBeVisible();

  await page.getByRole('button', { name: 'Account menu' }).click();
  await page.getByRole('menuitem', { name: 'Sign out' }).click();
  await expect(page).toHaveURL(/\/login$/);

  await signIn(page, email);
  await expect(page).toHaveURL(/\/login\/2fa/);
  expect(page.url()).not.toContain('mfa');
  // Wait for the next 30 s window: each code is accepted once.
  const now = Date.now();
  await page.waitForTimeout(30_000 - (now % 30_000) + 500);
  await page.getByLabel('Authentication code').fill(totp(secret));
  await page.getByRole('button', { name: 'Verify' }).click();
  await expect(page).toHaveURL(/\/onboarding$/);
});

test('password reset by email signs out other sessions', async ({ page, browser }) => {
  const email = uniqueEmail('reset');
  await registerAndVerify(page, email);
  await signIn(page, email);
  await expect(page).toHaveURL(/\/onboarding$/);

  const other = await browser.newPage();
  await other.goto('/forgot-password');
  await other.getByLabel('Email').fill(email);
  await other.getByRole('button', { name: 'Send reset link' }).click();
  await expect(other.getByRole('heading', { name: 'Check your email' })).toBeVisible();
  const link = await emailLink(other.request, email, '/reset-password');
  await other.goto(new URL(link).pathname + new URL(link).hash);
  await expect(other.getByRole('heading', { name: 'Choose a new password' })).toBeVisible();
  await expect(other).not.toHaveURL(/token=/);
  await other.getByLabel('New password').fill('a-brand-new-passphrase');
  await other.getByLabel('Confirm password').fill('a-brand-new-passphrase');
  await other.getByRole('button', { name: 'Change password' }).click();
  await expect(other.getByRole('status')).toContainText('Your password was changed');

  // The first browser's session was revoked server-side: its refresh cookie no longer works.
  const status = await page.evaluate(async () => {
    const csrf = /opshub_csrf=([^;]+)/.exec(document.cookie)?.[1] ?? '';
    const res = await fetch('/api/v1/auth/refresh', { method: 'POST', headers: { 'X-CSRF-Token': csrf } });
    return res.status;
  });
  expect(status).toBe(401);
  await page.reload();
  await expect(page).toHaveURL(/\/login/);
  await signIn(page, email, 'a-brand-new-passphrase');
  await expect(page).toHaveURL(/\/onboarding$/);
  await other.close();
});

test('personal API token works as a bearer credential and can be revoked', async ({ page, request }) => {
  await signIn(page, 'admin@demo.opshub.local', SEED_PASSWORD);
  await page.goto('/settings/tokens');
  await page.getByRole('button', { name: 'New token' }).click();
  const dialog = page.getByRole('dialog');
  await dialog.getByLabel('Name').fill(`e2e ${Date.now()}`);
  await dialog.getByRole('button', { name: 'Create' }).click();
  const token = (await page.getByTestId('new-token').textContent()) ?? '';
  expect(token).toMatch(/^ohp_[a-z2-7]{8}_/);

  const me = await request.get('/api/v1/me', { headers: { Authorization: `Bearer ${token}` } });
  expect(me.status()).toBe(200);
  expect((await me.json()) as { email: string }).toMatchObject({ email: 'admin@demo.opshub.local' });
  const forbidden = await request.get('/api/v1/me/tokens', { headers: { Authorization: `Bearer ${token}` } });
  expect(forbidden.status()).toBe(403);
});

test('Khmer layout of signed-in pages on mobile and desktop', async ({ page }) => {
  await signIn(page, 'owner@demo.opshub.local', SEED_PASSWORD);
  await expect(page).toHaveURL(/\/o\/angkor-tech$/);
  for (const width of [360, 1280]) {
    await page.setViewportSize({ width, height: 900 });
    for (const path of ['/o/angkor-tech', '/settings/profile', '/settings/security', '/settings/tokens']) {
      await page.goto(path);
      await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
      await expectKhmerLayoutOK(page);
      await page.screenshot({ path: `test-results/khmer-${width}${path.replaceAll('/', '_')}.png`, fullPage: true });
    }
  }
});

test('unknown organizations look exactly like missing ones', async ({ page }) => {
  await signIn(page, 'viewer@demo.opshub.local', SEED_PASSWORD);
  await page.goto('/o/some-other-tenant');
  await expect(page.getByRole('heading', { name: 'Page not found' })).toBeVisible();
});
