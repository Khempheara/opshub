import { useTranslation } from 'react-i18next';
import { NavLink, Outlet } from 'react-router';
import { cn } from '@/lib/utils';
import { LanguageSwitcher } from './LanguageSwitcher';

export function AppShell() {
  const { t } = useTranslation();
  const navClass = ({ isActive }: { isActive: boolean }) =>
    cn('rounded-md px-3 py-1.5 text-sm', isActive ? 'bg-accent font-medium' : 'text-muted-foreground hover:text-foreground');

  return (
    <div className="min-h-dvh">
      <a
        href="#main"
        className="bg-background sr-only rounded-md focus:not-sr-only focus:fixed focus:top-2 focus:left-2 focus:z-50 focus:px-3 focus:py-2"
      >
        {t('nav.skipToContent')}
      </a>
      <header className="bg-background/80 sticky top-0 z-40 border-b backdrop-blur">
        <div className="mx-auto flex max-w-6xl items-center gap-4 px-4 py-2">
          <NavLink to="/" className="font-heading text-lg font-bold">
            {t('appName')}
          </NavLink>
          <nav className="flex flex-1 items-center gap-1">
            <NavLink to="/" end className={navClass}>
              {t('nav.home')}
            </NavLink>
            <NavLink to="/settings/preferences" className={navClass}>
              {t('nav.preferences')}
            </NavLink>
            <a href="/docs" className={navClass({ isActive: false })}>
              {t('nav.apiDocs')}
            </a>
          </nav>
          <LanguageSwitcher />
        </div>
      </header>
      <main id="main" className="mx-auto max-w-6xl px-4 py-8">
        <Outlet />
      </main>
    </div>
  );
}
