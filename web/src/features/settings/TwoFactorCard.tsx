import { useQueryClient } from '@tanstack/react-query';
import { ShieldCheck, ShieldOff } from 'lucide-react';
import QRCode from 'qrcode';
import { useEffect, useState } from 'react';
import { useForm } from 'react-hook-form';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';
import { getSession, updateSessionUser } from '@/auth/session';
import { CopyButton } from '@/components/common/CopyButton';
import { PasswordField, TextField } from '@/components/common/Field';
import { ErrorState, FormError, LoadingState } from '@/components/common/States';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog';
import {
  disableTwoFactor,
  enableTwoFactor,
  getMe,
  getGetTwoFactorStatusQueryKey,
  regenerateRecoveryCodes,
  setupTwoFactor,
  useGetTwoFactorStatus,
} from '@/lib/api/generated/account/account';
import type { TOTPSetup } from '@/lib/api/generated/model';
import { errorMessage } from '@/lib/api/errors';

async function refreshUser() {
  if (getSession().status === 'authenticated') updateSessionUser(await getMe());
}

/** Recovery codes, shown exactly once, with copy and download. */
function RecoveryCodesDialog({ codes, onClose }: { codes: string[]; onClose: () => void }) {
  const { t } = useTranslation(['settings', 'common']);
  const text = codes.join('\n');
  const download = () => {
    const url = URL.createObjectURL(new Blob([text + '\n'], { type: 'text/plain' }));
    const a = Object.assign(document.createElement('a'), { href: url, download: 'opshub-recovery-codes.txt' });
    a.click();
    URL.revokeObjectURL(url);
  };
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('security.twoFactor.codesTitle')}</DialogTitle>
          <DialogDescription>{t('security.twoFactor.codesBody')}</DialogDescription>
        </DialogHeader>
        <ul className="bg-muted grid grid-cols-2 gap-2 rounded-md p-4 font-mono text-sm" data-testid="recovery-codes">
          {codes.map((c) => (
            <li key={c}>{c}</li>
          ))}
        </ul>
        <DialogFooter className="flex-wrap gap-2">
          <CopyButton value={text} />
          <Button variant="outline" size="sm" onClick={download}>
            {t('common:actions.download')}
          </Button>
          <Button onClick={onClose}>{t('security.twoFactor.codesSaved')}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/** Three-step enrollment: confirm password → scan QR → verify a code. */
function SetupDialog({ onDone, onCancel, initial }: { onDone: (codes: string[]) => void; onCancel: () => void; initial: TOTPSetup | null }) {
  const { t } = useTranslation(['settings', 'auth', 'common']);
  const [setup, setSetup] = useState<TOTPSetup | null>(initial);
  const [qr, setQr] = useState<string | null>(null);
  const [error, setError] = useState<unknown>(null);
  const form = useForm<{ password: string; code: string }>({ defaultValues: { password: '', code: '' } });

  useEffect(() => {
    if (!setup) return;
    // Rendered locally: the secret never leaves the browser for a QR service.
    void QRCode.toDataURL(setup.otpauth_uri, { margin: 1, width: 192 }).then(setQr);
  }, [setup]);

  const begin = async ({ password }: { password: string }) => {
    setError(null);
    try {
      setSetup(await setupTwoFactor({ password }));
    } catch (err) {
      setError(err);
    }
  };
  const confirm = async ({ code }: { code: string }) => {
    setError(null);
    try {
      const res = await enableTwoFactor({ code: code.replace(/\s/g, '') });
      onDone(res.recovery_codes);
    } catch (err) {
      setError(err);
      form.setValue('code', '');
    }
  };

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) onCancel();
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('security.twoFactor.setupTitle')}</DialogTitle>
          <DialogDescription>{setup ? t('security.twoFactor.setupStep2') : t('security.twoFactor.setupStep1')}</DialogDescription>
        </DialogHeader>
        {!setup ? (
          <form noValidate className="space-y-4" onSubmit={(e) => void form.handleSubmit(begin)(e)}>
            <PasswordField label={t('auth:fields.password')} autoComplete="current-password" autoFocus {...form.register('password')} />
            <FormError error={error} />
            <DialogFooter>
              <Button type="button" variant="outline" onClick={onCancel}>
                {t('common:actions.cancel')}
              </Button>
              <Button type="submit" disabled={form.formState.isSubmitting}>
                {t('common:actions.continue')}
              </Button>
            </DialogFooter>
          </form>
        ) : (
          <form noValidate className="space-y-4" onSubmit={(e) => void form.handleSubmit(confirm)(e)}>
            <div className="flex justify-center">
              {qr ? <img src={qr} alt={t('security.twoFactor.qrAlt')} width={192} height={192} className="rounded-md bg-white p-2" /> : <LoadingState rows={1} className="w-48" />}
            </div>
            <div className="space-y-2">
              <p className="text-muted-foreground text-sm">{t('security.twoFactor.manualEntry')}</p>
              <div className="flex flex-wrap items-center gap-2">
                <code className="bg-muted rounded px-2 py-1 text-sm break-all" data-testid="totp-secret">
                  {setup.secret}
                </code>
                <CopyButton value={setup.secret} />
              </div>
            </div>
            <TextField
              label={t('security.twoFactor.setupStep3')}
              inputMode="numeric"
              autoComplete="one-time-code"
              maxLength={7}
              className="font-mono tracking-widest"
              autoFocus
              {...form.register('code', { required: true })}
            />
            <FormError error={error} />
            <DialogFooter>
              <Button type="button" variant="outline" onClick={onCancel}>
                {t('common:actions.cancel')}
              </Button>
              <Button type="submit" disabled={form.formState.isSubmitting}>
                {t('auth:twoFactor.submit')}
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  );
}

/** Asks for a TOTP code (and the password when turning 2FA off). */
function CodeDialog({
  title,
  description,
  withPassword,
  destructive,
  onSubmit,
  onCancel,
}: {
  title: string;
  description: string;
  withPassword: boolean;
  destructive?: boolean;
  onSubmit: (v: { password: string; code: string }) => Promise<void>;
  onCancel: () => void;
}) {
  const { t } = useTranslation(['settings', 'auth', 'common']);
  const [error, setError] = useState<unknown>(null);
  const form = useForm<{ password: string; code: string }>({ defaultValues: { password: '', code: '' } });
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) onCancel();
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
        </DialogHeader>
        <form
          noValidate
          className="space-y-4"
          onSubmit={(e) =>
            void form.handleSubmit(async (v) => {
              setError(null);
              try {
                await onSubmit(v);
              } catch (err) {
                setError(err);
              }
            })(e)
          }
        >
          {withPassword && <PasswordField label={t('auth:fields.password')} autoComplete="current-password" {...form.register('password')} />}
          <TextField label={t('auth:fields.code')} inputMode="numeric" autoComplete="one-time-code" maxLength={7} className="font-mono tracking-widest" {...form.register('code', { required: true })} />
          <FormError error={error} />
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onCancel}>
              {t('common:actions.cancel')}
            </Button>
            <Button type="submit" variant={destructive ? 'destructive' : 'default'} disabled={form.formState.isSubmitting}>
              {t('common:actions.confirm')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

type Mode = null | 'setup' | 'disable' | 'regenerate';

export function TwoFactorCard() {
  const { t } = useTranslation(['settings', 'common']);
  const queryClient = useQueryClient();
  const status = useGetTwoFactorStatus();
  const [mode, setMode] = useState<Mode>(null);
  const [codes, setCodes] = useState<string[] | null>(null);
  const [ssoSetup, setSSOSetup] = useState<TOTPSetup | null>(null);
  const hasPassword = getSession().user?.has_password ?? true;

  const startSetup = async () => {
    // Accounts without a password (SSO-only) have nothing to re-confirm.
    if (!hasPassword) {
      try {
        setSSOSetup(await setupTwoFactor({}));
      } catch (err) {
        toast.error(errorMessage(err));
        return;
      }
    }
    setMode('setup');
  };

  const refresh = async () => {
    await queryClient.invalidateQueries({ queryKey: getGetTwoFactorStatusQueryKey() });
    await refreshUser();
  };

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex flex-wrap items-center gap-2">
          {t('security.twoFactor.title')}
          {status.data &&
            (status.data.enabled ? (
              <Badge className="bg-success text-white">
                <ShieldCheck aria-hidden />
                {t('security.twoFactor.enabled')}
              </Badge>
            ) : (
              <Badge variant="secondary">
                <ShieldOff aria-hidden />
                {t('security.twoFactor.disabled')}
              </Badge>
            ))}
        </CardTitle>
        <CardDescription>{t('security.twoFactor.description')}</CardDescription>
      </CardHeader>
      <CardContent className="space-y-3">
        {status.isPending ? (
          <LoadingState rows={1} />
        ) : status.isError ? (
          <ErrorState error={status.error} onRetry={() => void status.refetch()} />
        ) : status.data.enabled ? (
          <>
            <p className="text-muted-foreground text-sm">{t('security.twoFactor.remaining', { remaining: status.data.recovery_codes_remaining })}</p>
            <div className="flex flex-wrap gap-2">
              <Button variant="outline" onClick={() => { setMode('regenerate'); }}>
                {t('security.twoFactor.regenerate')}
              </Button>
              <Button variant="destructive" onClick={() => { setMode('disable'); }}>
                {t('security.twoFactor.disable')}
              </Button>
            </div>
          </>
        ) : (
          <Button onClick={() => void startSetup()}>{t('security.twoFactor.enable')}</Button>
        )}
      </CardContent>

      {mode === 'setup' && (
        <SetupDialog
          initial={ssoSetup}
          onCancel={() => { setMode(null); }}
          onDone={(c) => {
            setMode(null);
            setCodes(c);
            toast.success(t('security.twoFactor.enabledToast'));
            void refresh();
          }}
        />
      )}
      {mode === 'disable' && (
        <CodeDialog
          title={t('security.twoFactor.disableTitle')}
          description={t('security.twoFactor.disableBody')}
          withPassword={hasPassword}
          destructive
          onCancel={() => { setMode(null); }}
          onSubmit={async ({ password, code }) => {
            await disableTwoFactor({ password, code: code.replace(/\s/g, '') });
            setMode(null);
            toast.success(t('security.twoFactor.disabledToast'));
            await refresh();
          }}
        />
      )}
      {mode === 'regenerate' && (
        <CodeDialog
          title={t('security.twoFactor.regenerate')}
          description={t('security.twoFactor.codesBody')}
          withPassword={false}
          onCancel={() => { setMode(null); }}
          onSubmit={async ({ code }) => {
            const res = await regenerateRecoveryCodes({ code: code.replace(/\s/g, '') });
            setMode(null);
            setCodes(res.recovery_codes);
            await refresh();
          }}
        />
      )}
      {codes && (
        <RecoveryCodesDialog
          codes={codes}
          onClose={() => {
            setCodes(null);
          }}
        />
      )}
    </Card>
  );
}
