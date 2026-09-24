import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import i18n from 'i18next';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { createMemoryRouter, RouterProvider, useLocation } from 'react-router';
import { clearSession, getSession } from '@/auth/session';
import { LoginPage } from './LoginPage';

function Probe({ label }: { label: string }) {
  const loc = useLocation();
  return (
    <p data-testid="probe" data-search={loc.search}>
      {`${label} ${loc.search} ${JSON.stringify(loc.state)}`}
    </p>
  );
}

function setup(loginResponse: Response) {
  vi.spyOn(globalThis, 'fetch').mockImplementation((input) => {
    const url = input instanceof Request ? input.url : input.toString();
    if (url.endsWith('/api/v1/meta')) {
      return Promise.resolve(Response.json({ version: 't', locales: ['en', 'km'], default_locale: 'en', default_timezone: 'UTC', signup_enabled: true, sso_providers: ['github'] }));
    }
    return Promise.resolve(loginResponse);
  });
  const router = createMemoryRouter(
    [
      { path: '/login', element: <LoginPage /> },
      { path: '/login/2fa', element: <Probe label="2fa" /> },
      { path: '/dashboard', element: <Probe label="dashboard" /> },
    ],
    { initialEntries: ['/login?next=/dashboard'] },
  );
  render(
    <QueryClientProvider client={new QueryClient()}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
}

async function submit(email = 'dara@example.com', password = 'mekong-sunrise') {
  await userEvent.type(screen.getByLabelText('Email'), email);
  await userEvent.type(screen.getByLabelText('Password'), password);
  await userEvent.click(screen.getByRole('button', { name: 'Sign in' }));
}

afterEach(async () => {
  vi.restoreAllMocks();
  clearSession();
  await i18n.changeLanguage('en');
});

describe('LoginPage', () => {
  it('signs in and returns to the requested page', async () => {
    const user = { id: 'u', email: 'dara@example.com', display_name: 'Dara', locale: 'en', timezone: 'UTC', khmer_numerals: false, email_verified: true, two_factor_enabled: false, has_password: true, is_platform_admin: false, created_at: '', version: 1 };
    setup(Response.json({ status: 'authenticated', tokens: { access_token: 'a', token_type: 'Bearer', expires_in: 900, expires_at: new Date(Date.now() + 9e5).toISOString(), user } }));
    await submit();
    expect(await screen.findByTestId('probe')).toHaveTextContent('dashboard');
    expect(getSession().status).toBe('authenticated');
  });

  it('hands the MFA challenge to the 2FA page via router state, not the URL', async () => {
    setup(Response.json({ status: 'mfa_required', mfa: { token: 'challenge-123', expires_at: '' } }));
    await submit();
    const probe = await screen.findByTestId('probe');
    expect(probe).toHaveTextContent('2fa ?next=%2Fdashboard {"mfaToken":"challenge-123"}');
    expect(probe.dataset.search).not.toContain('challenge-123');
  });

  it('shows how long the account is locked', async () => {
    setup(Response.json({ error: { code: 'ACCOUNT_LOCKED', message: '', details: { retry_after_seconds: 1800 } } }, { status: 423 }));
    await submit();
    expect(await screen.findByRole('alert')).toHaveTextContent('Try again in 30 minutes.');
  });

  it('validates before calling the API and shows server errors in Khmer', async () => {
    setup(Response.json({ error: { code: 'INVALID_CREDENTIALS', message: '', details: {} } }, { status: 401 }));
    await userEvent.click(screen.getByRole('button', { name: 'Sign in' }));
    expect(await screen.findAllByText('This field is required.')).toHaveLength(2);

    await i18n.changeLanguage('km');
    await userEvent.type(screen.getByLabelText('អ៊ីមែល'), 'dara@example.com');
    await userEvent.type(screen.getByLabelText('ពាក្យសម្ងាត់'), 'wrong-password');
    await userEvent.click(screen.getByRole('button', { name: 'ចូលគណនី' }));
    expect(await screen.findByText('អ៊ីមែល ឬពាក្យសម្ងាត់មិនត្រឹមត្រូវ។')).toBeInTheDocument();
  });

  it('offers the enabled SSO providers', async () => {
    setup(Response.json({}));
    const link = await screen.findByRole('link', { name: 'Continue with GitHub' });
    expect(link).toHaveAttribute('href', '/api/v1/auth/sso/github/start?next=%2Fdashboard');
  });
});
