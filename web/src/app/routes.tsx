import type { RouteObject } from 'react-router';
import { AppShell } from '@/components/layout/AppShell';
import { AuthLayout } from '@/components/layout/AuthLayout';
import { LoginPage } from '@/features/auth/LoginPage';
import { NotFoundPage } from '@/pages/NotFoundPage';
import { OrgRoute, RedirectIfAuthenticated, RequireAuth, RootRedirect } from './guards';

// The sign-in page is eagerly loaded; everything else is code-split per page.
const page = <T extends string>(load: () => Promise<Record<T, React.ComponentType>>, name: T) => ({
  lazy: async () => ({ Component: (await load())[name] }),
});

export const routes: RouteObject[] = [
  {
    element: <AuthLayout />,
    children: [
      {
        element: <RedirectIfAuthenticated />,
        children: [
          { path: 'login', element: <LoginPage /> },
          { path: 'login/2fa', ...page(() => import('@/features/auth/TwoFactorPage'), 'TwoFactorPage') },
          { path: 'register', ...page(() => import('@/features/auth/RegisterPage'), 'RegisterPage') },
          { path: 'forgot-password', ...page(() => import('@/features/auth/ForgotPasswordPage'), 'ForgotPasswordPage') },
        ],
      },
      // Reachable whether or not someone is signed in.
      { path: 'verify-email', ...page(() => import('@/features/auth/VerifyEmailPage'), 'VerifyEmailPage') },
      { path: 'reset-password', ...page(() => import('@/features/auth/ResetPasswordPage'), 'ResetPasswordPage') },
      { path: 'auth/sso-complete', ...page(() => import('@/features/auth/SSOCompletePage'), 'SSOCompletePage') },
      { path: 'invitations/accept', ...page(() => import('@/features/org/AcceptInvitationPage'), 'AcceptInvitationPage') },
    ],
  },
  {
    element: <RequireAuth />,
    children: [
      { index: true, element: <RootRedirect /> },
      {
        element: <AppShell />,
        children: [
          { path: 'onboarding', ...page(() => import('@/features/org/OnboardingPage'), 'OnboardingPage') },
          {
            path: 'o/:orgSlug',
            element: <OrgRoute />,
            children: [
              { index: true, ...page(() => import('@/features/org/OrgHomePage'), 'OrgHomePage') },
              { path: 'members', ...page(() => import('@/features/org/MembersPage'), 'MembersPage') },
              { path: 'teams', ...page(() => import('@/features/org/TeamsPage'), 'TeamsPage') },
              { path: 'teams/:teamId', ...page(() => import('@/features/org/TeamDetailPage'), 'TeamDetailPage') },
              { path: 'settings', ...page(() => import('@/features/org/OrgSettingsPage'), 'OrgSettingsPage') },
              { path: 'projects', ...page(() => import('@/features/project/ProjectsPage'), 'ProjectsPage') },
              {
                path: 'projects/:projectId',
                ...page(() => import('@/features/project/ProjectLayout'), 'ProjectLayout'),
                children: [
                  { index: true, ...page(() => import('@/features/pipeline/RunsPage'), 'RunsPage') },
                  { path: 'runs/:runId', ...page(() => import('@/features/pipeline/RunPage'), 'RunPage') },
                  { path: 'settings', ...page(() => import('@/features/project/ProjectSettingsPage'), 'ProjectSettingsPage') },
                  { path: 'environments', ...page(() => import('@/features/project/EnvironmentsPage'), 'EnvironmentsPage') },
                  { path: 'repository', ...page(() => import('@/features/project/RepositoryPage'), 'RepositoryPage') },
                  { path: 'access', ...page(() => import('@/features/project/AccessPage'), 'AccessPage') },
                ],
              },
            ],
          },
          { path: 'settings/profile', ...page(() => import('@/features/settings/ProfilePage'), 'ProfilePage') },
          { path: 'settings/security', ...page(() => import('@/features/settings/SecurityPage'), 'SecurityPage') },
          { path: 'settings/tokens', ...page(() => import('@/features/settings/TokensPage'), 'TokensPage') },
          { path: 'settings/organizations', ...page(() => import('@/features/settings/OrganizationsPage'), 'OrganizationsPage') },
          { path: '*', element: <NotFoundPage /> },
        ],
      },
    ],
  },
];
