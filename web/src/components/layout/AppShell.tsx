import { Menu, PanelLeftClose, PanelLeftOpen } from 'lucide-react';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, Outlet } from 'react-router';
import { Button } from '@/components/ui/button';
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle, SheetTrigger } from '@/components/ui/sheet';
import { cn } from '@/lib/utils';
import { LanguageSwitcher } from './LanguageSwitcher';
import { OrgSwitcher } from './OrgSwitcher';
import { SidebarNav } from './Sidebar';
import { ThemeToggle } from './ThemeToggle';
import { UserMenu } from './UserMenu';

const COLLAPSED_KEY = 'opshub.sidebarCollapsed';

function readCollapsed(): boolean {
  try {
    return localStorage.getItem(COLLAPSED_KEY) === '1';
  } catch {
    return false;
  }
}

/** Signed-in layout: collapsible sidebar (drawer on mobile) + top bar. */
export function AppShell() {
  const { t } = useTranslation();
  const [collapsed, setCollapsed] = useState(readCollapsed);
  const [drawerOpen, setDrawerOpen] = useState(false);

  const toggle = () => {
    setCollapsed((c) => {
      try {
        localStorage.setItem(COLLAPSED_KEY, c ? '0' : '1');
      } catch {
        // not critical
      }
      return !c;
    });
  };

  return (
    <div className="flex min-h-dvh">
      <a
        href="#main"
        className="bg-background sr-only rounded-md focus:not-sr-only focus:fixed focus:top-2 focus:left-2 focus:z-50 focus:px-3 focus:py-2"
      >
        {t('nav.skipToContent')}
      </a>

      <aside
        className={cn(
          'bg-card sticky top-0 hidden h-dvh shrink-0 flex-col border-e transition-[width] md:flex',
          collapsed ? 'w-16' : 'w-60',
        )}
      >
        <div className={cn('flex min-h-14 items-center border-b px-4', collapsed && 'justify-center px-0')}>
          <Link to="/" className="font-heading text-lg font-bold">
            {collapsed ? 'O' : t('appName')}
          </Link>
        </div>
        <div className="flex-1 overflow-y-auto">
          <SidebarNav collapsed={collapsed} />
        </div>
        <div className="border-t p-2">
          <Button variant="ghost" size="icon" onClick={toggle} aria-label={t('nav.toggleSidebar')} aria-expanded={!collapsed}>
            {collapsed ? <PanelLeftOpen aria-hidden /> : <PanelLeftClose aria-hidden />}
          </Button>
        </div>
      </aside>

      <div className="flex min-w-0 flex-1 flex-col">
        <header className="bg-background/80 sticky top-0 z-40 flex min-h-14 items-center gap-2 border-b px-3 backdrop-blur sm:px-4">
          <Sheet open={drawerOpen} onOpenChange={setDrawerOpen}>
            <SheetTrigger asChild>
              <Button variant="ghost" size="icon" className="md:hidden" aria-label={t('nav.openMenu')}>
                <Menu aria-hidden />
              </Button>
            </SheetTrigger>
            <SheetContent side="left" className="w-72 p-0">
              <SheetHeader className="border-b">
                <SheetTitle>{t('appName')}</SheetTitle>
                <SheetDescription className="sr-only">{t('nav.main')}</SheetDescription>
              </SheetHeader>
              <SidebarNav
                onNavigate={() => {
                  setDrawerOpen(false);
                }}
              />
            </SheetContent>
          </Sheet>
          <OrgSwitcher />
          <div className="ms-auto flex shrink-0 items-center gap-1 sm:gap-2">
            <LanguageSwitcher />
            <ThemeToggle />
            <UserMenu />
          </div>
        </header>
        <main id="main" tabIndex={-1} className="mx-auto w-full max-w-5xl flex-1 px-4 py-6 outline-none sm:py-8">
          <Outlet />
        </main>
      </div>
    </div>
  );
}
