import { useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate, useSearchParams } from 'react-router';
import { safeNext } from '@/app/navigation';
import { broadcastLogin, refreshSession } from '@/auth/session';
import { LoadingState } from '@/components/common/States';

/** Landing page after an SSO callback: the API has set the session cookie; exchange it for tokens. */
export function SSOCompletePage() {
  const { t } = useTranslation('auth');
  const navigate = useNavigate();
  const [params] = useSearchParams();

  useEffect(() => {
    const next = safeNext(params.get('next'));
    void refreshSession().then((ok) => {
      if (ok) broadcastLogin();
      void navigate(ok ? next : '/login?error=SSO_FAILED', { replace: true });
    });
  }, [navigate, params]);

  return (
    <div className="space-y-4">
      <p className="text-muted-foreground text-sm">{t('sso.completing')}</p>
      <LoadingState rows={1} />
    </div>
  );
}
