import { useQueryClient } from '@tanstack/react-query';
import { ArrowLeft, KeyRound, Pencil, RefreshCw, Trash2 } from 'lucide-react';
import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useNavigate, useParams } from 'react-router';
import { toast } from 'sonner';
import { useCurrentOrg } from '@/app/org';
import { OrgAction, usePermissions } from '@/app/permissions';
import { ConfirmDialog } from '@/components/common/ConfirmDialog';
import { CopyButton } from '@/components/common/CopyButton';
import { ErrorState, LoadingState } from '@/components/common/States';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog';
import { useFormat } from '@/i18n/useFormat';
import { errorMessage, hasCode } from '@/lib/api/errors';
import type { AgentToken, Asset, Certificate, MetricPoint } from '@/lib/api/generated/model';
import {
  checkAssetCertificate,
  deleteAsset,
  getGetAssetCertificateQueryKey,
  getGetAssetQueryKey,
  getListAssetsQueryKey,
  issueAgentToken,
  useGetAsset,
  useGetAssetCertificate,
  useGetAssetMetrics,
} from '@/lib/api/generated/infrastructure/infrastructure';
import { NotFoundPage } from '@/pages/NotFoundPage';
import { AssetDialog } from './AssetDialog';
import { LineChart, type ChartPoint } from './LineChart';
import { kindIcon } from './kinds';
import { AssetStatusLabel } from './status';

const HOUR = 3600 * 1000;
const RANGES = [
  { key: '1h', ms: HOUR },
  { key: '24h', ms: 24 * HOUR },
  { key: '7d', ms: 7 * 24 * HOUR },
  { key: '30d', ms: 30 * 24 * HOUR },
  { key: '90d', ms: 90 * 24 * HOUR },
] as const;
type RangeKey = (typeof RANGES)[number]['key'];

/** Placeholder shown in commands where the operator's install path goes. */
const TOKEN_FILE = '/etc/opshub/agent-token';

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="grid gap-1 py-2 sm:grid-cols-[10rem_1fr] sm:gap-4">
      <dt className="text-muted-foreground text-sm">{label}</dt>
      <dd className="min-w-0 text-sm break-words">{children}</dd>
    </div>
  );
}

/** Shows a new agent token once, with the commands that run the agent. */
function AgentTokenDialog({ token, onClose }: { token: AgentToken | null; onClose: () => void }) {
  const { t } = useTranslation('infra');
  const origin = window.location.origin;
  const binary = token
    ? `install -m 600 /dev/null ${TOKEN_FILE}\nprintf '%s' '${token.token}' > ${TOKEN_FILE}\nopshub-runner agent --url ${origin} --token-file ${TOKEN_FILE}`
    : '';
  const docker = token
    ? `docker run -d --name opshub-agent --restart unless-stopped \\\n  -v /proc:/host/proc:ro -v /:/host:ro \\\n  -e OPSHUB_AGENT_TOKEN=${token.token} \\\n  opshub-runner agent --url ${origin} \\\n  --proc /host/proc --disk /host --hostname "$(hostname)"`
    : '';
  return (
    <Dialog
      open={token !== null}
      onOpenChange={(o) => {
        if (!o) onClose();
      }}
    >
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{t('agent.tokenTitle')}</DialogTitle>
          <DialogDescription>{t('agent.tokenBody')}</DialogDescription>
        </DialogHeader>
        {token && (
          <div className="min-w-0 space-y-4">
            <div className="space-y-2">
              <code className="bg-muted block rounded-md p-3 font-mono text-sm break-all" dir="ltr" data-testid="agent-token">
                {token.token}
              </code>
              <CopyButton value={token.token} />
            </div>
            <div className="space-y-2">
              <p className="text-sm font-medium">{t('agent.binary')}</p>
              <pre className="bg-muted overflow-x-auto rounded-md p-3 font-mono text-xs" dir="ltr">
                {binary}
              </pre>
              <CopyButton value={binary} label={t('agent.copyCommand')} />
            </div>
            <div className="space-y-2">
              <p className="text-sm font-medium">{t('agent.docker')}</p>
              <pre className="bg-muted overflow-x-auto rounded-md p-3 font-mono text-xs" dir="ltr">
                {docker}
              </pre>
              <CopyButton value={docker} label={t('agent.copyCommand')} />
              <p className="text-muted-foreground text-xs">{t('agent.linuxOnly')}</p>
            </div>
          </div>
        )}
        <DialogFooter>
          <Button onClick={onClose}>{t('agent.done')}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function AgentCard({ asset, canManage }: { asset: Asset; canManage: boolean }) {
  const { t } = useTranslation('infra');
  const fmt = useFormat();
  const queryClient = useQueryClient();
  const [token, setToken] = useState<AgentToken | null>(null);
  // installed: a token exists; reporting: the agent has sent a heartbeat with it.
  const agent = asset.agent?.installed ? asset.agent : null;
  const reporting = agent?.last_heartbeat_at != null;

  const issue = async () => {
    try {
      const tok = await issueAgentToken(asset.id);
      setToken(tok);
      await queryClient.invalidateQueries({ queryKey: getGetAssetQueryKey(asset.id) });
    } catch (err) {
      toast.error(errorMessage(err));
    }
  };

  return (
    <Card>
      <CardHeader className="flex flex-row flex-wrap items-center justify-between gap-2">
        <CardTitle>{t('agent.title')}</CardTitle>
        {canManage &&
          (agent ? (
            <ConfirmDialog
              trigger={
                <Button variant="outline" size="sm">
                  <KeyRound aria-hidden />
                  {t('agent.rotate')}
                </Button>
              }
              title={t('agent.rotateTitle')}
              description={t('agent.rotateBody')}
              confirmLabel={t('agent.rotate')}
              onConfirm={issue}
            />
          ) : (
            <Button size="sm" onClick={() => void issue()}>
              <KeyRound aria-hidden />
              {t('agent.issue')}
            </Button>
          ))}
      </CardHeader>
      <CardContent>
        {!agent ? (
          <p className="text-muted-foreground text-sm">{canManage ? t('agent.noneManage') : t('agent.none')}</p>
        ) : (
          <dl className="divide-y">
            <Row label={t('agent.token')}>
              <span className="font-mono text-xs" dir="ltr">
                {agent.token_prefix}…
              </span>
            </Row>
            {reporting ? (
              <>
                <Row label={t('agent.hostname')}>
                  <span dir="ltr">{agent.hostname || '—'}</span>
                </Row>
                <Row label={t('agent.platform')}>
                  <span dir="ltr">
                    {agent.os}/{agent.arch} · {agent.version}
                  </span>
                </Row>
                <Row label={t('agent.lastSeen')}>{agent.last_heartbeat_at ? fmt.relative(agent.last_heartbeat_at) : '—'}</Row>
              </>
            ) : (
              <Row label={t('agent.state')}>{t('agent.waiting')}</Row>
            )}
          </dl>
        )}
      </CardContent>
      <AgentTokenDialog
        token={token}
        onClose={() => {
          setToken(null);
        }}
      />
    </Card>
  );
}

function toChart(points: MetricPoint[], avg: 'cpu_pct' | 'mem_pct' | 'disk_pct', max: 'cpu_max' | 'mem_max' | 'disk_max'): ChartPoint[] {
  return points.map((p) => ({ t: Date.parse(p.t), avg: p[avg], max: p[max] }));
}

function MetricsCard({ asset }: { asset: Asset }) {
  const { t } = useTranslation('infra');
  const fmt = useFormat();
  const [range, setRange] = useState<RangeKey>('24h');
  const [now, setNow] = useState(() => Date.now());
  // Slide the window forward while the page is open.
  useEffect(() => {
    const id = window.setInterval(() => {
      setNow(Date.now());
    }, 60_000);
    return () => {
      window.clearInterval(id);
    };
  }, []);
  const ms = RANGES.find((r) => r.key === range)?.ms ?? 24 * HOUR;
  // Whole minutes keep the query key stable between renders; rounding "to" up keeps the
  // current minute's samples in range.
  const to = Math.ceil(now / 60_000) * 60_000;
  const from = to - ms;
  const series = useGetAssetMetrics(asset.id, { from: new Date(from).toISOString(), to: new Date(to).toISOString() }, { query: { placeholderData: (prev) => prev } });

  const summary = (key: 'cpu_pct' | 'mem_pct' | 'disk_pct') => {
    const vals = (series.data?.points ?? []).map((p) => p[key]).filter((v): v is number => v !== null);
    if (vals.length === 0) return t('metrics.noData');
    const avg = vals.reduce((a, b) => a + b, 0) / vals.length;
    return t('metrics.summary', { avg: fmt.number(avg, { maximumFractionDigits: 1 }), max: fmt.number(Math.max(...vals), { maximumFractionDigits: 1 }) });
  };

  return (
    <Card>
      <CardHeader className="flex flex-row flex-wrap items-center justify-between gap-2">
        <CardTitle>{t('metrics.title')}</CardTitle>
        <div role="group" aria-label={t('metrics.range')} className="flex flex-wrap gap-1">
          {RANGES.map((r) => (
            <Button
              key={r.key}
              size="sm"
              variant={range === r.key ? 'default' : 'outline'}
              aria-pressed={range === r.key}
              onClick={() => {
                setRange(r.key);
                setNow(Date.now());
              }}
            >
              {t(`metrics.ranges.${r.key}`)}
            </Button>
          ))}
        </div>
      </CardHeader>
      <CardContent className="space-y-6">
        {series.isPending ? (
          <LoadingState />
        ) : series.isError ? (
          <ErrorState error={series.error} onRetry={() => void series.refetch()} />
        ) : series.data.points.length === 0 ? (
          <p className="text-muted-foreground text-sm">{asset.agent?.last_heartbeat_at ? t('metrics.noData') : t('metrics.noAgent')}</p>
        ) : (
          <>
            <LineChart
              title={t('metrics.cpu')}
              points={toChart(series.data.points, 'cpu_pct', 'cpu_max')}
              from={from}
              to={to}
              summary={summary('cpu_pct')}
              avgLabel={t('metrics.avg')}
              maxLabel={t('metrics.max')}
            />
            <LineChart
              title={t('metrics.mem')}
              points={toChart(series.data.points, 'mem_pct', 'mem_max')}
              from={from}
              to={to}
              summary={summary('mem_pct')}
              tone="text-violet-600 dark:text-violet-400"
              avgLabel={t('metrics.avg')}
              maxLabel={t('metrics.max')}
            />
            <LineChart
              title={t('metrics.disk')}
              points={toChart(series.data.points, 'disk_pct', 'disk_max')}
              from={from}
              to={to}
              summary={summary('disk_pct')}
              tone="text-amber-600 dark:text-amber-400"
              avgLabel={t('metrics.avg')}
              maxLabel={t('metrics.max')}
            />
            <p className="text-muted-foreground text-xs">
              {t(series.data.source === 'hourly' ? 'metrics.sourceHourly' : 'metrics.sourceRaw', {
                step: fmt.duration(series.data.step_seconds * 1000),
              })}
            </p>
          </>
        )}
      </CardContent>
    </Card>
  );
}

export function CertificateDetails({ cert }: { cert: Certificate }) {
  const { t } = useTranslation('infra');
  const fmt = useFormat();
  return (
    <dl className="divide-y">
      <Row label={t('certificates.columns.status')}>
        <AssetStatusLabel status={cert.status} />
        {cert.error && <p className="text-destructive mt-1 text-xs">{cert.error}</p>}
      </Row>
      {cert.not_after && (
        <Row label={t('certificates.columns.expires')}>
          {fmt.dateTime(cert.not_after, { dateStyle: 'medium', timeStyle: 'short' })}
          {cert.days_left !== null && <span className="text-muted-foreground"> · {t('certificates.daysLeft', { count: cert.days_left })}</span>}
        </Row>
      )}
      {cert.subject && (
        <Row label={t('certificates.subject')}>
          <span dir="ltr">{cert.subject}</span>
        </Row>
      )}
      {cert.issuer && (
        <Row label={t('certificates.issuer')}>
          <span dir="ltr">{cert.issuer}</span>
        </Row>
      )}
      {cert.dns_names.length > 0 && (
        <Row label={t('certificates.names')}>
          <div className="flex flex-wrap gap-1">
            {cert.dns_names.map((n) => (
              <Badge key={n} variant="secondary" className="font-mono">
                {n}
              </Badge>
            ))}
          </div>
        </Row>
      )}
      {cert.fingerprint && (
        <Row label={t('certificates.fingerprint')}>
          <span className="font-mono text-xs break-all" dir="ltr">
            {cert.fingerprint}
          </span>
        </Row>
      )}
      <Row label={t('certificates.checked')}>{fmt.relative(cert.last_checked_at)}</Row>
    </dl>
  );
}

function CertificateCard({ asset, canManage }: { asset: Asset; canManage: boolean }) {
  const { t } = useTranslation('infra');
  const queryClient = useQueryClient();
  const cert = useGetAssetCertificate(asset.id);
  const [checking, setChecking] = useState(false);

  const check = async () => {
    setChecking(true);
    try {
      const c = await checkAssetCertificate(asset.id);
      queryClient.setQueryData(getGetAssetCertificateQueryKey(asset.id), { certificate: c });
      await queryClient.invalidateQueries({ queryKey: getGetAssetQueryKey(asset.id) });
      if (c.error) toast.error(c.error);
      else toast.success(t('certificates.checkedOk'));
    } catch (err) {
      toast.error(errorMessage(err));
    } finally {
      setChecking(false);
    }
  };

  return (
    <Card>
      <CardHeader className="flex flex-row flex-wrap items-center justify-between gap-2">
        <CardTitle>{t('certificates.cardTitle')}</CardTitle>
        {canManage && (
          <Button size="sm" variant="outline" disabled={checking} onClick={() => void check()}>
            <RefreshCw aria-hidden className={checking ? 'animate-spin' : undefined} />
            {t('certificates.checkNow')}
          </Button>
        )}
      </CardHeader>
      <CardContent>
        {cert.isPending ? (
          <LoadingState rows={2} />
        ) : cert.isError ? (
          <ErrorState error={cert.error} onRetry={() => void cert.refetch()} />
        ) : cert.data.certificate === null ? (
          <p className="text-muted-foreground text-sm">{t('certificates.notChecked')}</p>
        ) : (
          <CertificateDetails cert={cert.data.certificate} />
        )}
      </CardContent>
    </Card>
  );
}

/** /o/:orgSlug/infrastructure/:assetId */
export function AssetPage() {
  const { t } = useTranslation(['infra', 'common']);
  const { assetId = '' } = useParams();
  const org = useCurrentOrg();
  const perms = usePermissions(org.id);
  const canManage = perms.can(OrgAction.infraManage);
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const fmt = useFormat();
  const [editing, setEditing] = useState(false);
  const asset = useGetAsset(assetId, { query: { refetchInterval: 30_000 } });

  if (asset.isPending) return <LoadingState />;
  if (asset.isError) {
    if (hasCode(asset.error, 'ASSET_NOT_FOUND', 'NOT_FOUND')) return <NotFoundPage />;
    return <ErrorState error={asset.error} onRetry={() => void asset.refetch()} />;
  }
  const a = asset.data;
  const Icon = kindIcon[a.kind];
  const back = `/o/${org.slug}/infrastructure`;

  const remove = async () => {
    try {
      await deleteAsset(a.id);
      queryClient.removeQueries({ queryKey: getGetAssetQueryKey(a.id) });
      await queryClient.invalidateQueries({ queryKey: getListAssetsQueryKey(org.id) });
      toast.success(t('assets.deleted'));
      await navigate(back);
    } catch (err) {
      toast.error(errorMessage(err));
    }
  };

  return (
    <div className="space-y-6">
      <Link to={back} className="text-muted-foreground hover:text-foreground inline-flex items-center gap-1 text-sm">
        <ArrowLeft aria-hidden className="size-4 rtl:rotate-180" />
        {t('assets.back')}
      </Link>
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div className="min-w-0 space-y-1">
          <h1 className="flex items-center gap-2 text-2xl font-semibold tracking-tight">
            <Icon aria-hidden className="text-muted-foreground size-6 shrink-0" />
            <span className="break-all">{a.name}</span>
          </h1>
          <div className="flex flex-wrap items-center gap-3">
            <AssetStatusLabel status={a.status} />
            <span className="text-muted-foreground text-sm">{t(`kinds.${a.kind}`)}</span>
          </div>
        </div>
        {canManage && (
          <div className="flex flex-wrap gap-2">
            <Button
              variant="outline"
              onClick={() => {
                setEditing(true);
              }}
            >
              <Pencil aria-hidden />
              {t('assets.edit')}
            </Button>
            <ConfirmDialog
              trigger={
                <Button variant="outline">
                  <Trash2 aria-hidden />
                  {t('common:actions.delete')}
                </Button>
              }
              title={t('assets.deleteTitle', { name: a.name })}
              description={t('assets.deleteBody')}
              confirmLabel={t('common:actions.delete')}
              onConfirm={remove}
            />
          </div>
        )}
      </div>

      <Card>
        <CardHeader>
          <CardTitle>{t('assets.details')}</CardTitle>
        </CardHeader>
        <CardContent>
          <dl className="divide-y">
            <Row label={t('assets.form.address')}>
              <span className="font-mono text-xs" dir="ltr">
                {a.address || '—'}
                {a.kind === 'domain' && a.tls_port !== 443 ? `:${String(a.tls_port)}` : ''}
              </span>
            </Row>
            {a.description && <Row label={t('assets.form.description')}>{a.description}</Row>}
            {a.tags.length > 0 && (
              <Row label={t('assets.form.tags')}>
                <div className="flex flex-wrap gap-1">
                  {a.tags.map((tag) => (
                    <Badge key={tag} variant="secondary" className="font-mono">
                      {tag}
                    </Badge>
                  ))}
                </div>
              </Row>
            )}
            {Object.entries(a.metadata).map(([k, v]) => (
              <Row key={k} label={k}>
                <span dir="ltr">{v}</span>
              </Row>
            ))}
            <Row label={t('assets.updated')}>{fmt.relative(a.updated_at)}</Row>
          </dl>
        </CardContent>
      </Card>

      {a.kind === 'server' && (
        <>
          <AgentCard asset={a} canManage={canManage} />
          <MetricsCard asset={a} />
        </>
      )}
      {a.kind === 'domain' && <CertificateCard asset={a} canManage={canManage} />}

      {editing && (
        <AssetDialog
          orgId={org.id}
          orgSlug={org.slug}
          asset={a}
          open
          onOpenChange={(o) => {
            if (!o) setEditing(false);
          }}
        />
      )}
    </div>
  );
}
