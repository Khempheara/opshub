import { Outlet } from 'react-router';
import { useTranslation } from 'react-i18next';
import { LanguageSwitcher } from './LanguageSwitcher';
import { ThemeToggle } from './ThemeToggle';

/** Centered layout for sign-in, registration and recovery pages. */
export function AuthLayout() {
  const { t } = useTranslation();
  return (
    <div className="bg-muted/40 flex min-h-dvh flex-col">
      <header className="flex items-center justify-between gap-2 px-4 py-3">
        <span className="font-heading text-lg font-bold">{t('appName')}</span>
        <div className="flex items-center gap-2">
          <LanguageSwitcher />
          <ThemeToggle />
        </div>
      </header>
      <main id="main" className="flex flex-1 items-start justify-center px-4 py-8 sm:items-center">
        <div className="bg-card text-card-foreground w-full max-w-md rounded-xl border p-6 shadow-sm sm:p-8">
          <Outlet />
        </div>
      </main>
    </div>
  );
}
