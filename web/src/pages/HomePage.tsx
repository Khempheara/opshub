import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { useFormat } from '@/i18n/useFormat';
import { errorMessage } from '@/lib/api/errors';
import { useGetMeta } from '@/lib/api/generated/system/system';

const MODULES = [
  'auth',
  'rbac',
  'projects',
  'pipelines',
  'runners',
  'deployments',
  'infrastructure',
  'secrets',
  'monitoring',
  'logs',
  'audit',
  'dashboard',
] as const;

export function HomePage() {
  const { t } = useTranslation(['home', 'common']);
  const fmt = useFormat();
  const meta = useGetMeta({ query: { retry: 1 } });

  return (
    <div className="space-y-8">
      <section className="space-y-2">
        <h1 className="text-3xl font-bold">{t('home:title')}</h1>
        <p className="text-muted-foreground max-w-2xl">{t('home:intro')}</p>
      </section>

      <div className="grid gap-4 md:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle>{t('home:systemStatus')}</CardTitle>
          </CardHeader>
          <CardContent className="space-y-3">
            <div className="flex items-center justify-between gap-4">
              <span>{t('home:api')}</span>
              <StatusBadge state={meta.isPending ? 'loading' : meta.isError ? 'offline' : 'online'} />
            </div>
            {meta.data && (
              <p className="text-muted-foreground text-sm">
                {t('common:status.version', { version: meta.data.version })}
              </p>
            )}
            {meta.isError && (
              <div className="flex items-center justify-between gap-4">
                <p role="alert" className="text-destructive text-sm">
                  {errorMessage(meta.error)}
                </p>
                <Button variant="outline" size="sm" onClick={() => void meta.refetch()}>
                  {t('common:actions.retry')}
                </Button>
              </div>
            )}
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>{t('home:localeDemo.title')}</CardTitle>
          </CardHeader>
          <CardContent>
            <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2 text-sm">
              <dt className="text-muted-foreground">{t('home:localeDemo.now')}</dt>
              <dd data-testid="demo-now">{fmt.dateTime(new Date(), { dateStyle: 'full', timeStyle: 'short' })}</dd>
              <dt className="text-muted-foreground">{t('home:localeDemo.timezone')}</dt>
              <dd className="font-mono">{fmt.prefs.timeZone}</dd>
              <dt className="text-muted-foreground">{t('home:localeDemo.number')}</dt>
              <dd data-testid="demo-number">{fmt.number(1234567.89)}</dd>
              <dt className="text-muted-foreground">{t('home:localeDemo.duration')}</dt>
              <dd>{fmt.duration(83_000)}</dd>
            </dl>
          </CardContent>
        </Card>
      </div>

      <section className="space-y-4">
        <h2 className="text-xl font-semibold">{t('home:modulesTitle')}</h2>
        <ul className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {MODULES.map((m) => (
            <li key={m}>
              <Card className="gap-1 p-4">
                <CardTitle className="text-base">{t(`common:modules.${m}`)}</CardTitle>
                <CardDescription>{t('home:comingSoon')}</CardDescription>
              </Card>
            </li>
          ))}
        </ul>
      </section>
    </div>
  );
}

function StatusBadge({ state }: { state: 'loading' | 'online' | 'offline' }) {
  const { t } = useTranslation();
  const color = { loading: 'bg-muted-foreground', online: 'bg-success', offline: 'bg-destructive' }[state];
  return (
    <span className="inline-flex items-center gap-2 text-sm" data-testid="api-status">
      <span aria-hidden className={`size-2 rounded-full ${color}`} />
      {t(`status.${state}`)}
    </span>
  );
}
