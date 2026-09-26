import { createHmac, randomUUID } from 'node:crypto';
import { expect, type APIRequestContext, type Page } from '@playwright/test';

export const MAILPIT = process.env.E2E_MAILPIT_URL ?? 'http://localhost:8025';
export const SEED_PASSWORD = process.env.E2E_SEED_PASSWORD ?? 'angkor-wat-sunrise-2026';
export const PASSWORD = 'tonle-sap-monsoon-2026';

export const uniqueEmail = (prefix: string) => `${prefix}-${randomUUID().slice(0, 8)}@e2e.opshub.local`;

/** Waits for the newest email to `to` and returns the first link in it. */
export async function emailLink(request: APIRequestContext, to: string, pathPart: string): Promise<string> {
  let link = '';
  await expect
    .poll(
      async () => {
        const search = await request.get(`${MAILPIT}/api/v1/search`, { params: { query: `to:"${to}"` } });
        const { messages } = (await search.json()) as { messages: { ID: string }[] };
        if (messages.length === 0) return '';
        const msg = await request.get(`${MAILPIT}/api/v1/message/${messages[0]?.ID ?? ''}`);
        const { Text } = (await msg.json()) as { Text: string };
        link = new RegExp(`https?://\\S*${pathPart}#token=\\S+`).exec(Text)?.[0] ?? '';
        return link;
      },
      { timeout: 20_000, message: `email with ${pathPart} link to ${to}` },
    )
    .not.toBe('');
  return link;
}

/** RFC 6238 TOTP (SHA-1, 6 digits, 30 s) from a base32 secret. */
export function totp(secret: string, at = Date.now()): string {
  const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567';
  let bits = '';
  for (const c of secret.replace(/=+$/, '').toUpperCase()) bits += alphabet.indexOf(c).toString(2).padStart(5, '0');
  const key = Buffer.from(bits.match(/.{8}/g)?.map((b) => parseInt(b, 2)) ?? []);
  const counter = Buffer.alloc(8);
  counter.writeBigUInt64BE(BigInt(Math.floor(at / 1000 / 30)));
  const h = createHmac('sha1', key).update(counter).digest();
  const off = (h[h.length - 1] ?? 0) & 0x0f;
  return ((h.readUInt32BE(off) & 0x7fffffff) % 1_000_000).toString().padStart(6, '0');
}

/** Registers through the UI in English and confirms the email via Mailpit. */
export async function registerAndVerify(page: Page, email: string, name = 'E2E User') {
  await page.goto('/register');
  await page.getByRole('button', { name: 'EN' }).click();
  await page.getByLabel('Full name').fill(name);
  await page.getByLabel('Email').fill(email);
  await page.getByLabel('Password', { exact: true }).fill(PASSWORD);
  await page.getByRole('button', { name: 'Create account' }).click();
  await expect(page.getByRole('heading', { name: 'Check your email' })).toBeVisible();
  const link = await emailLink(page.request, email, '/verify-email');
  await page.goto(new URL(link).pathname + new URL(link).hash);
  await expect(page.getByRole('status')).toContainText('Your email is confirmed');
}

export async function signIn(page: Page, email: string, password = PASSWORD) {
  await page.goto('/login');
  await page.getByLabel('Email').fill(email);
  await page.getByLabel('Password', { exact: true }).fill(password);
  await page.getByRole('button', { name: /^(Sign in|ចូលគណនី)$/ }).click();
  // Wait until the app has moved on (to the destination or the 2FA step) so the session
  // cookies are set before the test navigates elsewhere.
  await page.waitForURL((url) => url.pathname !== '/login');
}

/** Registers a new user who creates (and so owns) a fresh organization; returns its slug. */
export async function newOwnerWithOrg(page: Page, name: string): Promise<string> {
  const email = uniqueEmail('owner');
  await registerAndVerify(page, email, 'Org Owner');
  await signIn(page, email);
  await expect(page).toHaveURL(/\/onboarding$/);
  const slug = `e2e-${Date.now().toString(36)}`;
  await page.getByLabel('Organization name').fill(name);
  await page.getByLabel('URL name').fill(slug);
  await page.getByRole('button', { name: 'Create organization' }).click();
  await expect(page).toHaveURL(new RegExp(`/o/${slug}$`));
  return slug;
}

/**
 * Switches the UI language like a person would, then waits for a signed-in user's profile to
 * save it (the switcher saves in the background): a page load right after the click could
 * otherwise cancel the save and bring the old language back.
 */
export async function switchLanguage(page: Page, lng: 'en' | 'km') {
  const saved = page
    .waitForResponse((r) => r.url().endsWith('/api/v1/me') && r.request().method() === 'PATCH', { timeout: 3000 })
    .catch(() => null); // not signed in, or the profile already had this language
  await page.getByRole('button', lng === 'km' ? { name: 'ខ្មែរ' } : { name: 'EN', exact: true }).click();
  await expect(page.locator('html')).toHaveAttribute('lang', lng);
  await saved;
}

/**
 * The seeded Payments API project's link in the projects list (the demo organization has other
 * projects too, such as checkout-web with the dashboard history).
 */
export function demoProjectLink(page: Page) {
  return page.getByTestId('project-list').getByRole('link', { name: /Payments API/ });
}
