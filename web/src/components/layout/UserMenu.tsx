import { useQueryClient } from '@tanstack/react-query';
import { KeyRound, LogOut, ShieldCheck, UserRound } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router';
import { clearSession, useSession } from '@/auth/session';
import { Avatar, AvatarFallback } from '@/components/ui/avatar';
import { Button } from '@/components/ui/button';
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu';
import { logout } from '@/lib/api/generated/auth/auth';

function initials(name: string): string {
  const parts = name.split(/[\s·]+/).filter(Boolean);
  return (
    parts
      .slice(0, 2)
      .map((p) => Array.from(p)[0] ?? '')
      .join('')
      .toUpperCase() || '?'
  );
}

export function UserMenu() {
  const { t } = useTranslation();
  const { user } = useSession();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  if (!user) return null;

  const signOut = async () => {
    try {
      await logout();
    } finally {
      clearSession(true);
      queryClient.clear();
      void navigate('/login', { replace: true });
    }
  };

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" size="icon" className="rounded-full" aria-label={t('userMenu.label')}>
          <Avatar className="size-8">
            <AvatarFallback>{initials(user.display_name)}</AvatarFallback>
          </Avatar>
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-64">
        <DropdownMenuLabel className="font-normal">
          <p className="truncate-khmer font-medium">{user.display_name}</p>
          <p className="text-muted-foreground truncate text-xs">{t('userMenu.signedInAs', { email: user.email })}</p>
        </DropdownMenuLabel>
        <DropdownMenuSeparator />
        <DropdownMenuItem onSelect={() => void navigate('/settings/profile')}>
          <UserRound aria-hidden className="size-4" />
          {t('nav.profile')}
        </DropdownMenuItem>
        <DropdownMenuItem onSelect={() => void navigate('/settings/security')}>
          <ShieldCheck aria-hidden className="size-4" />
          {t('nav.security')}
        </DropdownMenuItem>
        <DropdownMenuItem onSelect={() => void navigate('/settings/tokens')}>
          <KeyRound aria-hidden className="size-4" />
          {t('nav.tokens')}
        </DropdownMenuItem>
        <DropdownMenuSeparator />
        <DropdownMenuItem onSelect={() => void signOut()}>
          <LogOut aria-hidden className="size-4" />
          {t('userMenu.signOut')}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
