import { expect, test, type Page } from '@playwright/test';
import { expectKhmerLayoutOK } from '../khmer';
import { SEED_PASSWORD, signIn } from './helpers';

// Runs against the demo data from `make seed`: a succeeded run (approved production deploy),
// a failed feature branch, a run waiting for approval and a queued manual run.
test.describe.configure({ mode: 'serial' });

const PROJECT_LIST = '/o/angkor-tech/projects';

async function openDemoProject(page: Page) {
  await page.goto(PROJECT_LIST);
  await page.getByTestId('project-list').getByRole('link').first().click();
  await expect(page.getByTestId('runs-table')).toBeVisible();
}

// Demo runs by commit title and status (run numbers differ between fresh and re-seeded data).
const demo = {
  succeeded: { title: 'Add KHQR refunds', status: 'Succeeded' },
  failed: { title: 'Round refunds to the nearest riel', status: 'Failed' },
  waiting: { title: 'Bump card processor SDK', status: 'Waiting for approval' },
  queued: { title: 'Bump card processor SDK', status: 'Queued' },
};

function runRow(page: Page, run: { title: string; status: string }) {
  return page.getByTestId('runs-table').getByRole('row').filter({ hasText: run.title }).filter({ hasText: run.status });
}

async function openRun(page: Page, run: { title: string; status: string }) {
  await runRow(page, run).getByRole('link').first().click();
  await expect(page.getByRole('heading', { level: 2, name: /Run #/ })).toBeVisible();
}

test('seeded runs, a failed job and its log', async ({ page }) => {
  // The Admin's profile language is English (the demo Developer prefers Khmer).
  await signIn(page, 'admin@demo.opshub.local', SEED_PASSWORD);
  await openDemoProject(page);
  for (const run of Object.values(demo)) await expect(runRow(page, run)).toHaveCount(1);

  await openRun(page, demo.failed);
  await page.getByTestId('job-test').click();
  const panel = page.getByTestId('job-panel');
  await expect(panel).toContainText('A step exited with an error.');
  await expect(panel.getByTestId('job-log')).toContainText('expected 1250 riel, got 1249');
  await expect(page.getByTestId('job-build')).toContainText('Skipped');
  await expect(page).toHaveURL(/\?job=/);
});

test('a Viewer sees runs read-only and why they cannot approve', async ({ page }) => {
  await signIn(page, 'viewer@demo.opshub.local', SEED_PASSWORD);
  await openDemoProject(page);
  await expect(page.getByRole('button', { name: 'Run pipeline' })).toHaveCount(0);
  await openRun(page, demo.waiting);
  await expect(page.getByRole('button', { name: 'Cancel run' })).toHaveCount(0);
  await page.getByTestId('job-deploy-production').click();
  const card = page.getByTestId('approval-card');
  await expect(card).toContainText('0 of 1 approvals');
  await expect(card).toContainText("Your role on this project can't approve deployments.");
  await expect(card.getByRole('button', { name: 'Approve' })).toHaveCount(0);
});

test('an Admin approves the production deploy', async ({ page }) => {
  await signIn(page, 'admin@demo.opshub.local', SEED_PASSWORD);
  await openDemoProject(page);
  await openRun(page, demo.waiting);
  await page.getByTestId('job-deploy-production').click();
  const card = page.getByTestId('approval-card');
  await card.getByLabel('Comment (optional)').fill('Ship it');
  await card.getByRole('button', { name: 'Approve' }).click();
  await expect(card).toContainText('1 of 1 approvals');
  await expect(card).toContainText('approved — Ship it');
  await expect(page.getByTestId('job-panel')).toContainText('Waiting for a runner to pick up this job.');
  await expect(page.getByTestId('job-deploy-production')).toContainText('Queued');
});

test('the pipeline file checker reports problems with line numbers', async ({ page }) => {
  await signIn(page, 'admin@demo.opshub.local', SEED_PASSWORD);
  await openDemoProject(page);
  await page.getByRole('button', { name: 'Check pipeline file' }).click();
  const dialog = page.getByRole('dialog');
  await dialog.getByLabel('Pipeline file').fill('version: 1\nstages: [test]\njobs:\n  unit:\n    stage: tset\n    image: golang:1.23\n    steps: [go test ./...]\n');
  await dialog.getByRole('button', { name: 'Check' }).click();
  await expect(dialog.getByTestId('pipeline-problems')).toContainText('Unknown stage. Stages: test.');
  await expect(dialog.getByTestId('pipeline-problems')).toContainText('Line 5, column 12');
  await dialog.getByLabel('Pipeline file').fill('version: 1\nstages: [test]\njobs:\n  unit:\n    stage: test\n    image: golang:1.23\n    steps: [go test ./...]\n');
  await dialog.getByRole('button', { name: 'Check' }).click();
  await expect(dialog.getByText('Looks good: 1 jobs in 1 stages.')).toBeVisible();
});

test('an Admin cancels the queued run', async ({ page }) => {
  await signIn(page, 'admin@demo.opshub.local', SEED_PASSWORD);
  await openDemoProject(page);
  await openRun(page, demo.queued);
  await page.getByRole('button', { name: 'Cancel run' }).click();
  await page.getByRole('alertdialog').getByRole('button', { name: 'Cancel run' }).click();
  await expect(page.getByRole('heading', { level: 2 })).toContainText('Canceled');
  await expect(page.getByRole('button', { name: 'Re-run', exact: true })).toBeVisible();
});

test('Khmer layout of the pipeline pages on mobile and desktop', async ({ page }) => {
  await signIn(page, 'owner@demo.opshub.local', SEED_PASSWORD);
  await page.goto(PROJECT_LIST);
  await page.getByRole('button', { name: 'ខ្មែរ' }).click();
  await expect(page.locator('html')).toHaveAttribute('lang', 'km');
  const project = (await page.getByTestId('project-list').getByRole('link').first().getAttribute('href')) ?? '';
  await page.goto(project);
  const run = (await runRow(page, { title: demo.failed.title, status: 'បរាជ័យ' }).getByRole('link').first().getAttribute('href')) ?? '';
  for (const viewport of [
    { width: 375, height: 812 },
    { width: 1280, height: 800 },
  ]) {
    await page.setViewportSize(viewport);
    for (const path of [project, run]) {
      await page.goto(path);
      await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
      await expectKhmerLayoutOK(page);
    }
  }
});
