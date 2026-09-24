import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/button';
import { useGetMeta } from '@/lib/api/generated/system/system';

/** "Continue with …" buttons for the SSO providers enabled on this server. */
export function SSOButtons({ next }: { next: string }) {
  const { t } = useTranslation('auth');
  const meta = useGetMeta({ query: { staleTime: Infinity } });
  const providers = meta.data?.sso_providers ?? [];
  if (providers.length === 0) return null;
  return (
    <div className="space-y-3">
      <div className="grid gap-2">
        {providers.map((p) => (
          <Button key={p} variant="outline" asChild>
            {/* Full-page navigation: the provider redirects back to the API callback. */}
            <a href={`/api/v1/auth/sso/${p}/start?next=${encodeURIComponent(next)}`}>
              {t('sso.continueWith', { provider: t(`sso.providers.${p}`) })}
            </a>
          </Button>
        ))}
      </div>
      <div className="text-muted-foreground flex items-center gap-3 text-xs">
        <span className="bg-border h-px flex-1" />
        {t('sso.or')}
        <span className="bg-border h-px flex-1" />
      </div>
    </div>
  );
}
