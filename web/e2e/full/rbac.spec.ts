import { expect, test } from '@playwright/test';
import { expectKhmerLayoutOK } from '../khmer';
import { SEED_PASSWORD, emailLink, newOwnerWithOrg, registerAndVerify, signIn, uniqueEmail } from './helpers';

test.describe.configure({ mode: 'serial' });

test('invite by email → register → accept → member appears with the invited role', async ({ page, browser }) => {
  test.setTimeout(90_000);
  const slug = await newOwnerWithOrg(page, 'Tonle Sap Labs');
  const invitee = uniqueEmail('invitee');

  await page.goto(`/o/${slug}/members`);
  await page.getByRole('button', { name: 'Invite member' }).click();
  const dialog = page.getByRole('dialog');
  await dialog.getByLabel('Email').fill(invitee);
  await dialog.getByLabel('Role').selectOption('developer');
  await dialog.getByRole('button', { name: 'Send invitation' }).click();
  await expect(page.getByTestId('pending-invitations')).toContainText(invitee);

  // The invitee is a different person: a separate browser context with no session.
  const guest = await browser.newPage();
  const link = await emailLink(guest.request, invitee, '/invitations/accept');
  await guest.goto(new URL(link).pathname + new URL(link).hash);
  await expect(guest.getByText('Sign in or create an account with the email address')).toBeVisible();
  await expect(guest).toHaveURL(/\/invitations\/accept$/); // token removed from the address bar

  // registerAndVerify stays in this tab, so the pending invitation survives the detour.
  await registerAndVerify(guest, invitee, 'Invited Dev');
  await guest.getByRole('link', { name: 'Go to sign in' }).click();
  await expect(guest).toHaveURL(/\/login\?next=%2Finvitations%2Faccept$/);
  await guest.getByLabel('Email').fill(invitee);
  await guest.getByLabel('Password', { exact: true }).fill('tonle-sap-monsoon-2026');
  await guest.getByRole('button', { name: 'Sign in' }).click();

  await expect(guest.getByText('invited you to join Tonle Sap Labs as Developer')).toBeVisible();
  await guest.getByRole('button', { name: 'Accept invitation' }).click();
  await expect(guest).toHaveURL(new RegExp(`/o/${slug}$`));
  await guest.close();

  await page.reload();
  await expect(page.getByTestId(`member-${invitee}`)).toContainText('Developer');
  await expect(page.getByText('No pending invitations.')).toBeVisible();
});

test('the last owner cannot leave; teams can be created and staffed', async ({ page }) => {
  test.setTimeout(60_000);
  const slug = await newOwnerWithOrg(page, 'Kampot Pepper Ops');

  await page.goto(`/o/${slug}/settings`);
  await page.getByRole('button', { name: 'Leave organization' }).click();
  await page.getByRole('alertdialog').getByRole('button', { name: 'Leave organization' }).click();
  await expect(page.getByText('An organization needs at least one owner.')).toBeVisible();

  await page.goto(`/o/${slug}/teams`);
  await page.getByRole('button', { name: 'New team' }).click();
  await page.getByRole('dialog').getByLabel('Team name').fill('Release crew');
  await page.getByRole('dialog').getByRole('button', { name: 'New team' }).click();
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Release crew');
  await page.getByLabel('Choose a member').selectOption({ index: 1 }); // the owner themself
  await page.getByRole('button', { name: 'Add member' }).click();
  await expect(page.getByTestId('team-members').locator('li')).toHaveCount(1);
});

test('a viewer sees members but no management controls', async ({ page }) => {
  await signIn(page, 'viewer@demo.opshub.local', SEED_PASSWORD);
  await page.goto('/o/angkor-tech/members');
  await expect(page.getByTestId('members-table')).toContainText('owner@demo.opshub.local');
  await expect(page.getByRole('button', { name: 'Invite member' })).toHaveCount(0);
  await expect(page.getByRole('combobox', { name: /^Role for/ })).toHaveCount(0);
  await expect(page.getByTestId('pending-invitations')).toHaveCount(0);

  await page.goto('/o/angkor-tech/teams');
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Teams');
  await expect(page.getByRole('button', { name: 'New team' })).toHaveCount(0);

  await page.goto('/o/angkor-tech/settings');
  await expect(page.getByRole('button', { name: 'Leave organization' })).toBeVisible();
  await expect(page.getByLabel('Organization name')).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Delete organization' })).toHaveCount(0);
});

test('Khmer layout of the organization pages on mobile and desktop', async ({ page }) => {
  await signIn(page, 'owner@demo.opshub.local', SEED_PASSWORD);
  for (const viewport of [
    { width: 375, height: 812 },
    { width: 1280, height: 800 },
  ]) {
    await page.setViewportSize(viewport);
    for (const path of ['members', 'teams', 'settings']) {
      await page.goto(`/o/angkor-tech/${path}`);
      await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
      await expectKhmerLayoutOK(page);
    }
  }
});
