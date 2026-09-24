import { CheckCircle2, Circle } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router';
import { useCurrentOrg } from '@/app/org';
import { useSession } from '@/auth/session';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';

export function OrgHomePage() {
  const { t } = useTranslation(['home', 'common']);
  const org = useCurrentOrg();
  const { user } = useSession();

  const steps = [
    { done: true, label: t('steps.orgCreated') },
    {
      done: Boolean(user?.two_factor_enabled),
      label: user?.two_factor_enabled ? t('steps.secureAccountDone') : t('steps.secureAccount'),
      to: '/settings/security',
    },
    { done: false, label: t('steps.profile'), to: '/settings/profile', optional: true },
  ];

  return (
    <div className="space-y-8">
      <div className="space-y-2">
        <h1 className="text-2xl font-bold">{t('welcome', { org: org.name })}</h1>
        <Badge variant="secondary">{t('yourRole', { role: t(`common:roles.${org.role}`) })}</Badge>
      </div>

      <Card>
        <CardHeader>
          <CardTitle>{t('gettingStarted')}</CardTitle>
        </CardHeader>
        <CardContent>
          <ul className="divide-y">
            {steps.map((s) => (
              <li key={s.label} className="flex flex-wrap items-center gap-3 py-3">
                {s.done ? (
                  <CheckCircle2 aria-hidden className="text-success size-5 shrink-0" />
                ) : (
                  <Circle aria-hidden className="text-muted-foreground size-5 shrink-0" />
                )}
                <span className="min-w-0 flex-1">{s.label}</span>
                {s.to && (
                  <Button asChild variant="outline" size="sm">
                    <Link to={s.to}>{s.done || s.optional ? t('review') : t('setUp')}</Link>
                  </Button>
                )}
              </li>
            ))}
          </ul>
        </CardContent>
      </Card>
    </div>
  );
}
