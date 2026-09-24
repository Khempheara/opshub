import { CheckCircle2 } from 'lucide-react';
import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router';
import { ErrorState, LoadingState } from '@/components/common/States';
import { Button } from '@/components/ui/button';
import { verifyEmail } from '@/lib/api/generated/auth/auth';
import { useHashToken } from './tokenFromHash';

type State = { kind: 'verifying' } | { kind: 'done' } | { kind: 'error'; error: unknown } | { kind: 'missing' };

// The token is consumed once per page load, even under React StrictMode's double effects.
let pending: { token: string; promise: Promise<void> } | null = null;

export function VerifyEmailPage() {
  const { t } = useTranslation('auth');
  const hashToken = useHashToken();
  const [token] = useState(() => hashToken ?? pending?.token ?? null);
  const [state, setState] = useState<State>(token ? { kind: 'verifying' } : { kind: 'missing' });

  useEffect(() => {
    if (!token) return;
    if (pending?.token !== token) pending = { token, promise: verifyEmail({ token }) };
    pending.promise.then(
      () => {
        setState({ kind: 'done' });
      },
      (error: unknown) => {
        setState({ kind: 'error', error });
      },
    );
  }, [token]);

  return (
    <div className="space-y-4">
      <h1 className="text-2xl font-bold">{t('verify.title')}</h1>
      {state.kind === 'verifying' && (
        <>
          <p className="text-muted-foreground text-sm">{t('verify.verifying')}</p>
          <LoadingState rows={1} />
        </>
      )}
      {state.kind === 'done' && (
        <p role="status" className="flex items-start gap-2 text-sm">
          <CheckCircle2 aria-hidden className="text-success mt-1 size-4 shrink-0" />
          {t('verify.success')}
        </p>
      )}
      {state.kind === 'missing' && <p className="text-muted-foreground text-sm">{t('verify.missingToken')}</p>}
      {state.kind === 'error' && <ErrorState error={state.error} />}
      {state.kind !== 'verifying' && (
        <Button asChild className="w-full">
          <Link to="/login">{t('verify.goToLogin')}</Link>
        </Button>
      )}
    </div>
  );
}
