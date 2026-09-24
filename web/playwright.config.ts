import { defineConfig, devices } from '@playwright/test';

/**
 * Two suites:
 *  - mocked: UI behaviour and Khmer typography against the production build (vite preview);
 *    API calls are mocked per test. Needs no backend.
 *  - full: end-to-end against a running stack (`make dev`, `make seed`). Enabled when
 *    E2E_BASE_URL is set (`make e2e` sets it). Emails are read from Mailpit (E2E_MAILPIT_URL).
 */
const fullStack = process.env.E2E_BASE_URL;

export default defineConfig({
  testDir: './e2e',
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 2 : 0,
  reporter: process.env.CI ? [['github'], ['html', { open: 'never' }]] : 'list',
  use: { trace: 'retain-on-failure', screenshot: 'only-on-failure' },
  projects: [
    {
      name: 'mocked',
      testDir: './e2e/mocked',
      use: { ...devices['Desktop Chrome'], baseURL: 'http://localhost:4173' },
    },
    ...(fullStack
      ? [
          {
            name: 'full',
            testDir: './e2e/full',
            use: { ...devices['Desktop Chrome'], baseURL: fullStack },
          },
        ]
      : []),
  ],
  webServer: {
    command: 'npm run build && npx vite preview --port 4173 --strictPort',
    url: 'http://localhost:4173',
    reuseExistingServer: !process.env.CI,
    timeout: 120_000,
  },
});
