import { useTranslation } from 'react-i18next';
import { Link } from 'react-router';
import { Button } from '@/components/ui/button';

export function NotFoundPage() {
  const { t } = useTranslation();
  return (
    <div className="space-y-4 py-16 text-center">
      <h1 className="text-2xl font-bold">{t('notFound.title')}</h1>
      <p className="text-muted-foreground">{t('notFound.body')}</p>
      <Button asChild variant="outline">
        <Link to="/">{t('actions.backHome')}</Link>
      </Button>
    </div>
  );
}
