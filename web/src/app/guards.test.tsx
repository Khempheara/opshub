import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { clearSession, setSession } from '@/auth/session';
import { RedirectIfAuthenticated, RequireAuth } from './guards';

const user = {
  id: 'u1', email: 'a@b.co', display_name: 'A', locale: 'en' as const, timezone: 'UTC', khmer_numerals: false,
  email_verified: true, two_factor_enabled: false, has_password: true, is_platform_admin: false,
  created_at: '2026-01-01T00:00:00Z', version: 1,
};

function renderAt(path: string) {
  const router = createMemoryRouter(
    [
      { element: <RequireAuth />, children: [{ path: '/secret', element: <p>secret page</p> }] },
      { element: <RedirectIfAuthenticated />, children: [{ path: '/login', element: <p>login page</p> }] },
      { path: '/', element: <p>home page</p> },
    ],
    { initialEntries: [path] },
  );
  render(
    <QueryClientProvider client={new QueryClient()}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  return router;
}

afterEach(() => {
  clearSession();
});

describe('route guards', () => {
  it('sends anonymous users to login with a return path', () => {
    clearSession();
    const router = renderAt('/secret?x=1');
    expect(screen.getByText('login page')).toBeInTheDocument();
    expect(router.state.location.search).toBe('?next=%2Fsecret%3Fx%3D1');
  });

  it('does not remember the page after a deliberate sign-out', () => {
    clearSession(true);
    const router = renderAt('/secret');
    expect(screen.getByText('login page')).toBeInTheDocument();
    expect(router.state.location.search).toBe('');
  });

  it('lets signed-in users through and bounces them off the login page', () => {
    setSession({ access_token: 't', token_type: 'Bearer', expires_in: 900, expires_at: new Date(Date.now() + 9e5).toISOString(), user });
    renderAt('/secret');
    expect(screen.getByText('secret page')).toBeInTheDocument();
  });

  it('never redirects to another site after sign-in', () => {
    setSession({ access_token: 't', token_type: 'Bearer', expires_in: 900, expires_at: new Date(Date.now() + 9e5).toISOString(), user });
    const router = renderAt('/login?next=//evil.example');
    expect(router.state.location.pathname).toBe('/');
  });
});
