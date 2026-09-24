import { useQuery, useQueryClient } from '@tanstack/react-query';
import { UsersRound } from 'lucide-react';
import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useNavigate } from 'react-router';
import { toast } from 'sonner';
import { clearSession, useSession } from '@/auth/session';
import { ErrorState, FormError, LoadingState } from '@/components/common/States';
import { Button } from '@/components/ui/button';
import { useHashToken } from '@/features/auth/tokenFromHash';
import { useFormat } from '@/i18n/useFormat';
import { hasCode } from '@/lib/api/errors';
import { logout } from '@/lib/api/generated/auth/auth';
import { acceptInvitation, previewInvitation } from '@/lib/api/generated/members/members';
import { getListOrganizationsQueryKey } from '@/lib/api/generated/organizations/organizations';
import {
  ACCEPT_INVITATION_PATH,
  clearPendingInvitation,
  getPendingInvitation,
  setPendingInvitation,
} from './pendingInvitation';

const nextParam = `?next=${encodeURIComponent(ACCEPT_INVITATION_PATH)}`;

function useInvitationToken(): string | null {
  const fromHash = useHashToken();
  const [token] = useState(() => fromHash ?? getPendingInvitation());
  useEffect(() => {
    if (fromHash) setPendingInvitation(fromHash);
  }, [fromHash]);
  return token;
}

function Heading({ children }: { children: string }) {
  return (
    <div className="space-y-3 text-center">
      <UsersRound aria-hidden className="text-primary mx-auto size-10" />
      <h1 className="text-2xl font-bold">{children}</h1>
    </div>
  );
}

function SignInFirst() {
  const { t } = useTranslation(['org', 'auth']);
  return (
    <div className="space-y-6">
      <Heading>{t('accept.title')}</Heading>
      <p className="text-muted-foreground text-center text-sm">{t('accept.signInPrompt')}</p>
      <div className="grid gap-2">
        <Button asChild>
          <Link to={`/login${nextParam}`}>{t('auth:login.submit')}</Link>
        </Button>
        <Button asChild variant="outline">
          <Link to={`/register${nextParam}`}>{t('auth:register.submit')}</Link>
        </Button>
      </div>
    </div>
  );
}

function WrongAccount({ current, invited }: { current: string; invited: string }) {
  const { t } = useTranslation('org');
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const signOut = async () => {
    try {
      await logout();
    } finally {
      clearSession(true);
      queryClient.clear();
      // The invitation token stays pending, so signing in again returns here.
      void navigate(`/login${nextParam}`, { replace: true });
    }
  };
  return (
    <div className="space-y-4" role="alert">
      <p className="text-center text-sm">{t('accept.wrongAccount', { current, invited })}</p>
      <Button className="w-full" onClick={() => void signOut()}>
        {t('accept.signOut')}
      </Button>
    </div>
  );
}

function Invitation({ token }: { token: string }) {
  const { t } = useTranslation(['org', 'common']);
  const { user } = useSession();
  const fmt = useFormat();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const preview = useQuery({
    queryKey: ['invitation-preview', token],
    queryFn: () => previewInvitation({ token }),
    retry: false,
    staleTime: Infinity,
  });

  if (preview.isPending) {
    return (
      <div className="space-y-4">
        <Heading>{t('accept.title')}</Heading>
        <p className="text-muted-foreground text-center text-sm">{t('accept.loading')}</p>
        <LoadingState rows={1} />
      </div>
    );
  }
  if (preview.isError) {
    return (
      <div className="space-y-4">
        <Heading>{t('accept.title')}</Heading>
        <ErrorState error={preview.error} />
        <Button asChild variant="outline" className="w-full">
          <Link to="/" onClick={clearPendingInvitation}>
            {t('common:actions.backHome')}
          </Link>
        </Button>
      </div>
    );
  }

  const inv = preview.data;
  const invitedEmail = inv.email;
  if (user && user.email.toLowerCase() !== invitedEmail.toLowerCase()) {
    return (
      <div className="space-y-4">
        <Heading>{t('accept.title')}</Heading>
        <WrongAccount current={user.email} invited={invitedEmail} />
      </div>
    );
  }

  const role = t(`common:roles.${inv.role}`);
  const accept = async () => {
    setError(null);
    setBusy(true);
    try {
      const org = await acceptInvitation({ token });
      clearPendingInvitation();
      await queryClient.invalidateQueries({ queryKey: getListOrganizationsQueryKey() });
      toast.success(t('accept.joined', { org: org.name }));
      await navigate(`/o/${org.slug}`, { replace: true });
    } catch (err) {
      setError(err);
      setBusy(false);
    }
  };

  return (
    <div className="space-y-6">
      <Heading>{t('accept.title')}</Heading>
      <div className="space-y-2 text-center">
        <p>
          {inv.invited_by_name
            ? t('accept.body', { inviter: inv.invited_by_name, org: inv.organization_name, role })
            : t('accept.bodyNoInviter', { org: inv.organization_name, role })}
        </p>
        <p className="text-muted-foreground text-xs">
          {t('accept.sentTo', { email: invitedEmail })} · {t('invitations.expires', { time: fmt.relative(inv.expires_at) })}
        </p>
      </div>
      {hasCode(error, 'INVITATION_EMAIL_MISMATCH') && user ? (
        <WrongAccount current={user.email} invited={invitedEmail} />
      ) : (
        <FormError error={error} />
      )}
      <div className="grid gap-2">
        <Button onClick={() => void accept()} disabled={busy}>
          {t('accept.accept')}
        </Button>
        <Button asChild variant="ghost">
          <Link to="/" onClick={clearPendingInvitation}>
            {t('accept.decline')}
          </Link>
        </Button>
      </div>
    </div>
  );
}

/**
 * /invitations/accept#token=… — the link from an invitation email. Anonymous visitors are
 * asked to sign in or register first; the token waits in sessionStorage meanwhile.
 */
export function AcceptInvitationPage() {
  const { t } = useTranslation('org');
  const { status } = useSession();
  const token = useInvitationToken();

  if (!token) {
    return (
      <div className="space-y-4">
        <Heading>{t('accept.title')}</Heading>
        <p className="text-muted-foreground text-center text-sm" role="alert">
          {t('accept.missing')}
        </p>
      </div>
    );
  }
  if (status === 'loading') return <LoadingState rows={2} />;
  if (status === 'anonymous') return <SignInFirst />;
  return <Invitation token={token} />;
}
