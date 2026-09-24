import { zodResolver } from '@hookform/resolvers/zod';
import { useQueryClient } from '@tanstack/react-query';
import { CheckCircle2, ExternalLink, GitBranch, Webhook, XCircle } from 'lucide-react';
import { useState } from 'react';
import { useForm, useWatch } from 'react-hook-form';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';
import { z } from 'zod';
import { ConfirmDialog } from '@/components/common/ConfirmDialog';
import { CopyButton } from '@/components/common/CopyButton';
import { Field, PasswordField, TextField } from '@/components/common/Field';
import { EmptyState, ErrorState, FormError, LoadingState } from '@/components/common/States';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from '@/components/ui/card';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table';
import { selectClass } from '@/features/org/constants';
import { useFormat } from '@/i18n/useFormat';
import { applyFieldErrors, errorMessage, hasCode } from '@/lib/api/errors';
import { ProjectAction, type ConnectRepositoryResult, type Repository } from '@/lib/api/generated/model';
import {
  connectRepository,
  disconnectRepository,
  getGetRepositoryQueryKey,
  getListWebhookDeliveriesQueryKey,
  testRepository,
  useGetRepository,
  useListWebhookDeliveries,
} from '@/lib/api/generated/repositories/repositories';
import { cn } from '@/lib/utils';
import { canProject, useCurrentProject } from './context';

const schema = z.object({
  provider: z.enum(['github', 'gitlab']),
  base_url: z.union([z.literal(''), z.string().trim().regex(/^https:\/\/[^\s?#]+$/, 'errors:rules.https_url')]),
  full_name: z.string().trim().regex(/^[A-Za-z0-9_.-]+(\/[A-Za-z0-9_.-]+)+$/, 'errors:rules.repository'),
  access_token: z.string().trim().min(1, 'errors:rules.required'),
});
type Values = z.infer<typeof schema>;

function ConnectForm({ projectId, onConnected, onCancel }: { projectId: string; onConnected: (r: ConnectRepositoryResult) => void; onCancel?: () => void }) {
  const { t } = useTranslation(['project', 'common', 'errors']);
  const queryClient = useQueryClient();
  const [error, setError] = useState<unknown>(null);
  const form = useForm<Values>({ resolver: zodResolver(schema), defaultValues: { provider: 'github', base_url: '', full_name: '', access_token: '' } });
  const provider = useWatch({ control: form.control, name: 'provider' });
  const errs = form.formState.errors;
  const msg = (m?: string, type?: string) => (m ? (type === 'server' ? m : t(m as 'errors:rules.required')) : undefined);

  const onSubmit = async (v: Values) => {
    setError(null);
    try {
      const res = await connectRepository(projectId, { ...v, ...(v.base_url ? {} : { base_url: '' }) });
      form.reset({ ...v, access_token: '' });
      toast.success(t('repository.connected'));
      await queryClient.invalidateQueries({ queryKey: getGetRepositoryQueryKey(projectId) });
      await queryClient.invalidateQueries({ queryKey: getListWebhookDeliveriesQueryKey(projectId) });
      onConnected(res);
    } catch (err) {
      if (hasCode(err, 'GIT_REPO_NOT_FOUND')) form.setError('full_name', { type: 'server', message: errorMessage(err) });
      else if (hasCode(err, 'GIT_ACCESS_DENIED')) form.setError('access_token', { type: 'server', message: errorMessage(err) });
      else if (hasCode(err, 'SSRF_BLOCKED')) form.setError('base_url', { type: 'server', message: errorMessage(err) });
      else if (!applyFieldErrors(err, form.setError, ['provider', 'base_url', 'full_name', 'access_token'])) setError(err);
    }
  };

  return (
    <form noValidate className="space-y-4" onSubmit={(e) => void form.handleSubmit(onSubmit)(e)}>
      <Field label={t('repository.provider')}>
        {({ id }) => (
          <select id={id} className={cn(selectClass, 'w-full sm:max-w-xs')} {...form.register('provider')}>
            <option value="github">{t('repository.github')}</option>
            <option value="gitlab">{t('repository.gitlab')}</option>
          </select>
        )}
      </Field>
      <TextField
        label={t('repository.fullName')}
        hint={provider === 'github' ? t('repository.fullNameHintGithub') : t('repository.fullNameHintGitlab')}
        autoComplete="off"
        spellCheck={false}
        error={msg(errs.full_name?.message, errs.full_name?.type)}
        {...form.register('full_name')}
      />
      <PasswordField
        label={t('repository.token')}
        hint={provider === 'github' ? t('repository.tokenHintGithub') : t('repository.tokenHintGitlab')}
        autoComplete="off"
        spellCheck={false}
        error={msg(errs.access_token?.message, errs.access_token?.type)}
        {...form.register('access_token')}
      />
      <TextField
        label={t('repository.baseUrl')}
        hint={t('repository.baseUrlHint')}
        type="url"
        inputMode="url"
        autoComplete="off"
        spellCheck={false}
        error={msg(errs.base_url?.message, errs.base_url?.type)}
        {...form.register('base_url')}
      />
      <FormError error={error} />
      <div className="flex flex-wrap gap-2">
        <Button type="submit" disabled={form.formState.isSubmitting}>
          {form.formState.isSubmitting ? t('repository.connecting') : t('repository.connect')}
        </Button>
        {onCancel && (
          <Button type="button" variant="outline" onClick={onCancel}>
            {t('repository.cancelReplace')}
          </Button>
        )}
      </div>
    </form>
  );
}

/** Shown once after connecting when OpsHub couldn't install the webhook itself. */
function ManualSetup({ result, onDone }: { result: ConnectRepositoryResult; onDone: () => void }) {
  const { t } = useTranslation('project');
  const { webhook } = result;
  return (
    <Card className="border-amber-500/50" role="alert" data-testid="manual-webhook">
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <Webhook aria-hidden className="size-5" />
          {t('repository.manualTitle')}
        </CardTitle>
        <CardDescription>
          {webhook.reason && `${t(`repository.manualReasons.${webhook.reason}`)} `}
          {t('repository.manualIntro')}
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-3 pt-4">
        <CopyRow label={t('repository.webhookUrl')} value={webhook.url} />
        {webhook.secret && <CopyRow label={t('repository.secret')} value={webhook.secret} testId="webhook-secret" />}
        <ul className="text-muted-foreground list-disc space-y-1 ps-5 text-sm">
          <li>{t('repository.manualContentType')}</li>
          <li>{t('repository.manualEvents')}</li>
        </ul>
        <p className="text-sm font-medium">{t('repository.secretOnce')}</p>
      </CardContent>
      <CardFooter className="pt-4">
        <Button onClick={onDone}>{t('repository.manualDone')}</Button>
      </CardFooter>
    </Card>
  );
}

function CopyRow({ label, value, testId }: { label: string; value: string; testId?: string }) {
  return (
    <div className="space-y-1">
      <p className="text-sm font-medium">{label}</p>
      <div className="flex flex-wrap items-center gap-2">
        <code data-testid={testId} className="bg-muted min-w-0 flex-1 rounded-md px-2 py-1.5 font-mono text-xs break-all">
          {value}
        </code>
        <CopyButton value={value} />
      </div>
    </div>
  );
}

function RepositoryCard({ repo, canConnect, onReplace }: { repo: Repository; canConnect: boolean; onReplace: () => void }) {
  const { t } = useTranslation('project');
  const fmt = useFormat();
  const project = useCurrentProject();
  const queryClient = useQueryClient();
  const [testing, setTesting] = useState(false);

  const test = async () => {
    setTesting(true);
    try {
      const res = await testRepository(project.id);
      toast.success(t('repository.testOk', { branch: res.default_branch }));
      await queryClient.invalidateQueries({ queryKey: getGetRepositoryQueryKey(project.id) });
    } catch (err) {
      toast.error(errorMessage(err));
    } finally {
      setTesting(false);
    }
  };

  return (
    <Card data-testid="repository-card">
      <CardHeader>
        <CardTitle className="flex flex-wrap items-center gap-2">
          <span className="font-mono break-all">{repo.full_name}</span>
          <Badge variant="secondary">{repo.provider === 'github' ? t('repository.github') : t('repository.gitlab')}</Badge>
        </CardTitle>
        <CardDescription>
          <a href={repo.web_url} target="_blank" rel="noreferrer noopener" className="inline-flex items-center gap-1 break-all underline-offset-4 hover:underline">
            {repo.web_url}
            <ExternalLink aria-hidden className="size-3 shrink-0" />
          </a>
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4 pt-4">
        <dl className="grid gap-x-6 gap-y-3 text-sm sm:grid-cols-2">
          <div>
            <dt className="text-muted-foreground">{t('repository.branch')}</dt>
            <dd className="flex items-center gap-1 font-mono">
              <GitBranch aria-hidden className="size-4" />
              {repo.default_branch}
            </dd>
          </div>
          <div>
            <dt className="text-muted-foreground">{t('repository.webhook')}</dt>
            <dd>{repo.webhook_mode === 'automatic' ? t('repository.webhookAutomatic') : t('repository.webhookManual')}</dd>
          </div>
          <div>
            <dt className="text-muted-foreground">{t('repository.lastDelivery')}</dt>
            <dd>{repo.last_delivery_at ? fmt.relative(repo.last_delivery_at) : t('repository.never')}</dd>
          </div>
        </dl>
        <CopyRow label={t('repository.cloneUrl')} value={repo.clone_url} />
        {canConnect && <CopyRow label={t('repository.webhookUrl')} value={repo.webhook_url} />}
      </CardContent>
      {canConnect && (
        <CardFooter className="flex flex-wrap gap-2 pt-4">
          <Button variant="outline" disabled={testing} onClick={() => void test()}>
            {testing ? t('repository.testing') : t('repository.test')}
          </Button>
          <Button variant="outline" onClick={onReplace}>
            {t('repository.replace')}
          </Button>
          <ConfirmDialog
            trigger={<Button variant="destructive">{t('repository.disconnect')}</Button>}
            title={t('repository.disconnectTitle', { name: repo.full_name })}
            description={t('repository.disconnectBody')}
            confirmLabel={t('repository.disconnect')}
            onConfirm={async () => {
              try {
                await disconnectRepository(project.id);
                toast.success(t('repository.disconnected'));
              } catch (err) {
                toast.error(errorMessage(err));
              }
              await queryClient.invalidateQueries({ queryKey: getGetRepositoryQueryKey(project.id) });
            }}
          />
        </CardFooter>
      )}
    </Card>
  );
}

function Deliveries({ projectId }: { projectId: string }) {
  const { t } = useTranslation('project');
  const fmt = useFormat();
  const deliveries = useListWebhookDeliveries(projectId, { limit: 20 }, { query: { refetchInterval: 30_000 } });
  return (
    <section className="space-y-3" aria-labelledby="deliveries-title">
      <h3 id="deliveries-title" className="font-semibold">
        {t('repository.deliveries')}
      </h3>
      {deliveries.isPending ? (
        <LoadingState rows={2} />
      ) : deliveries.isError ? (
        <ErrorState error={deliveries.error} onRetry={() => void deliveries.refetch()} />
      ) : deliveries.data.items.length === 0 ? (
        <p className="text-muted-foreground text-sm">{t('repository.deliveriesEmpty')}</p>
      ) : (
        <Card className="gap-0 overflow-x-auto p-0">
          <Table data-testid="deliveries">
            <TableHeader>
              <TableRow>
                <TableHead>{t('repository.columns.event')}</TableHead>
                <TableHead>{t('repository.columns.ref')}</TableHead>
                <TableHead>{t('repository.columns.commit')}</TableHead>
                <TableHead>{t('repository.columns.signature')}</TableHead>
                <TableHead>{t('repository.columns.received')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {deliveries.data.items.map((d) => (
                <TableRow key={d.id}>
                  <TableCell className="font-mono text-xs">{d.event}</TableCell>
                  <TableCell className="font-mono text-xs">{d.ref.replace(/^refs\/(heads|tags)\//, '')}</TableCell>
                  <TableCell className="font-mono text-xs">{d.commit_sha.slice(0, 7)}</TableCell>
                  <TableCell>
                    {d.signature_valid ? (
                      <span className="text-success inline-flex items-center gap-1 text-sm">
                        <CheckCircle2 aria-hidden className="size-4" />
                        {t('repository.signatureValid')}
                      </span>
                    ) : (
                      <span className="text-destructive inline-flex items-center gap-1 text-sm">
                        <XCircle aria-hidden className="size-4" />
                        {t('repository.signatureInvalid')}
                      </span>
                    )}
                  </TableCell>
                  <TableCell className="text-sm whitespace-nowrap">{fmt.relative(d.received_at)}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Card>
      )}
    </section>
  );
}

export function RepositoryPage() {
  const { t } = useTranslation('project');
  const project = useCurrentProject();
  const canConnect = canProject(project, ProjectAction.repoconnect);
  const repo = useGetRepository(project.id, { query: { retry: (n, err) => !hasCode(err, 'REPOSITORY_NOT_FOUND') && n < 2 } });
  const [manual, setManual] = useState<ConnectRepositoryResult | null>(null);
  const [replacing, setReplacing] = useState(false);
  const notConnected = repo.isError && hasCode(repo.error, 'REPOSITORY_NOT_FOUND');

  const onConnected = (res: ConnectRepositoryResult) => {
    setReplacing(false);
    if (res.webhook.mode === 'manual') setManual(res);
  };

  return (
    <section className="max-w-3xl space-y-6" aria-labelledby="repository-title">
      <div>
        <h2 id="repository-title" className="text-lg font-semibold">
          {t('repository.title')}
        </h2>
        <p className="text-muted-foreground text-sm">{t('repository.description')}</p>
      </div>
      {manual && (
        <ManualSetup
          result={manual}
          onDone={() => {
            setManual(null);
          }}
        />
      )}
      {repo.isPending ? (
        <LoadingState />
      ) : notConnected || replacing ? (
        canConnect ? (
          <Card>
            <CardHeader>
              <CardTitle>{replacing ? t('repository.replaceTitle') : t('repository.connectTitle')}</CardTitle>
            </CardHeader>
            <CardContent className="pt-4">
              <ConnectForm
                projectId={project.id}
                onConnected={onConnected}
                onCancel={
                  replacing
                    ? () => {
                        setReplacing(false);
                      }
                    : undefined
                }
              />
            </CardContent>
          </Card>
        ) : (
          <EmptyState title={t('repository.notConnected')} description={t('repository.notConnectedReadOnly')} />
        )
      ) : repo.isError ? (
        <ErrorState error={repo.error} onRetry={() => void repo.refetch()} />
      ) : (
        <>
          <RepositoryCard
            repo={repo.data}
            canConnect={canConnect}
            onReplace={() => {
              setReplacing(true);
            }}
          />
          {canConnect && <Deliveries projectId={project.id} />}
        </>
      )}
    </section>
  );
}
