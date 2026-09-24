import type { RouteObject } from 'react-router';
import { AppShell } from '@/components/layout/AppShell';
import { HomePage } from '@/pages/HomePage';

// Pages other than the landing page are code-split.
export const routes: RouteObject[] = [
  {
    element: <AppShell />,
    children: [
      { index: true, element: <HomePage /> },
      {
        path: 'settings/preferences',
        lazy: async () => ({ Component: (await import('@/pages/PreferencesPage')).PreferencesPage }),
      },
      { path: '*', lazy: async () => ({ Component: (await import('@/pages/NotFoundPage')).NotFoundPage }) },
    ],
  },
];
