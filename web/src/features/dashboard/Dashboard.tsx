import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router';
import { EmptyState, ErrorState, LoadingState } from '@/components/common/States';
import { Card } from '@/components/ui/card';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table';
import { LineChart } from '@/features/infra/LineChart';
import { selectClass } from '@/features/org/constants';
import { useFormat } from '@/i18n/useFormat';
import { useGetDoraMetrics, useGetPipelineStats } from '@/lib/api/generated/dashboard/dashboard';
import { useListProjects } from '@/lib/api/generated/projects/projects';
import { useDisplayPrefs } from '@/preferences/store';
import { MetricCard } from './MetricCard';
import { StackedBars } from './StackedBars';
import {
  PERIODS,
  failureRateLevel,
  frequency,
  frequencyLevel,
  leadTimeLevel,
  niceMax,
  periodRange,
  projectRows,
  restoreLevel,
  type Period,
} from './dashboardView';

const REFRESH_MS = 60_000;

/** Organization overview: DORA metrics and pipeline statistics for the projects the caller can see. */
export function Dashboard({ orgId, orgSlug }: { orgId: string; orgSlug: string }) {
  const { t } = useTranslation(['dashboard', 'pipeline']);
  const fmt = useFormat();
  const { timeZone } = useDisplayPrefs();
  const projects = useListProjects(orgId, { limit: 200 }, { query: { staleTime: 60_000 } });
  const [project, setProject] = useState('');
  const [period, setPeriod] = useState<Period>('30d');
  const [anchor] = useState(() => Date.now());
  const params = useMemo(
    () => ({ ...periodRange(period, anchor), tz: timeZone, ...(project ? { project } : {}) }),
    [period, anchor, timeZone, project],
  );
  const query = { query: { refetchInterval: REFRESH_MS } };
  const pipelines = useGetPipelineStats(orgId, params, query);
  const dora = useGetDoraMetrics(orgId, params, query);

  const duration = (s: number | null) => (s === null ? null : fmt.duration(s * 1000));
  const percent = (r: number | null) => (r === null ? null : fmt.number(r, { style: 'percent', maximumFractionDigits: 1 }));

  const filters = (
    <div className="flex flex-wrap gap-2">
      <label className="sr-only" htmlFor="dashboard-project">
        {t('filters.project')}
      </label>
      <select
        id="dashboard-project"
        className={`${selectClass} w-auto max-w-64`}
        value={project}
        onChange={(e) => {
          setProject(e.target.value);
        }}
      >
        <option value="">{t('filters.allProjects')}</option>
        {(projects.data?.items ?? []).map((p) => (
          <option key={p.id} value={p.id}>
            {p.name}
          </option>
        ))}
      </select>
      <label className="sr-only" htmlFor="dashboard-period">
        {t('filters.period')}
      </label>
      <select
        id="dashboard-period"
        className={`${selectClass} w-auto`}
        value={period}
        onChange={(e) => {
          setPeriod(e.target.value as Period);
        }}
      >
        {PERIODS.map((p) => (
          <option key={p} value={p}>
            {t(`periods.${p}`)}
          </option>
        ))}
      </select>
    </div>
  );

  if (pipelines.isPending || dora.isPending) {
    return (
      <div className="space-y-6">
        {filters}
        <LoadingState />
      </div>
    );
  }
  if (pipelines.isError || dora.isError) {
    const error = pipelines.error ?? dora.error;
    return (
      <div className="space-y-6">
        {filters}
        <ErrorState error={error} onRetry={() => void Promise.all([pipelines.refetch(), dora.refetch()])} />
      </div>
    );
  }

  const p = pipelines.data;
  const d = dora.data;
  const freq = frequency(d.deployments.per_day);
  const rows = projectRows(p.projects, d.projects);
  const nothing = p.summary.runs === 0 && d.change_failure_rate.changes === 0;
  const maxDuration = niceMax(Math.max(0, ...p.trend.map((x) => x.duration_p95_s ?? 0)));

  return (
    <div className="space-y-8">
      {filters}

      <section className="space-y-3" aria-labelledby="dora-title">
        <div>
          <h2 id="dora-title" className="text-lg font-semibold">
            {t('dora.title')}
          </h2>
          <p className="text-muted-foreground text-sm">{t('dora.description')}</p>
        </div>
        <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
          <MetricCard
            testId="metric-frequency"
            title={t('dora.frequency.title')}
            value={d.deployments.succeeded > 0 ? t(`dora.frequency.${freq.unit}`, { value: fmt.number(freq.value, { maximumFractionDigits: 1 }) }) : null}
            none={t('dora.frequency.none')}
            detail={t('dora.frequency.detail', { count: d.deployments.succeeded })}
            level={frequencyLevel(d.deployments.per_day)}
          />
          <MetricCard
            testId="metric-lead-time"
            title={t('dora.leadTime.title')}
            value={duration(d.lead_time.median_s)}
            none={t('dora.leadTime.none')}
            detail={t('dora.leadTime.detail', { count: d.lead_time.samples })}
            level={leadTimeLevel(d.lead_time.median_s)}
          />
          <MetricCard
            testId="metric-failure-rate"
            title={t('dora.failureRate.title')}
            value={percent(d.change_failure_rate.rate)}
            none={t('dora.failureRate.none')}
            detail={t('dora.failureRate.detail', { failed: d.change_failure_rate.failed, count: d.change_failure_rate.changes })}
            level={failureRateLevel(d.change_failure_rate.rate)}
          />
          <MetricCard
            testId="metric-restore"
            title={t('dora.restore.title')}
            value={duration(d.time_to_restore.median_s)}
            none={d.time_to_restore.open > 0 ? t('dora.restore.noneOpen') : t('dora.restore.none')}
            detail={t('dora.restore.detail', { count: d.time_to_restore.restored })}
            level={restoreLevel(d.time_to_restore.median_s)}
            note={d.time_to_restore.open > 0 ? t('dora.restore.open', { count: d.time_to_restore.open }) : undefined}
          />
        </div>
        <div className="grid gap-3 lg:grid-cols-[minmax(0,2fr)_minmax(0,1fr)]">
          <Card className="p-4">
            <StackedBars
              title={t('dora.chart.title')}
              summary={t('dora.chart.summary', {
                succeeded: d.trend.reduce((n, x) => n + x.succeeded, 0),
                failed: d.trend.reduce((n, x) => n + x.failed_changes, 0),
              })}
              series={[
                { label: t('dora.chart.succeeded'), tone: 'fill-success bg-success' },
                { label: t('dora.chart.failed'), tone: 'fill-destructive bg-destructive' },
              ]}
              periods={d.trend.map((x) => ({ period: x.period, values: [x.succeeded, x.failed_changes] }))}
            />
          </Card>
          <MetricCard
            testId="metric-alerts"
            title={t('dora.alerts.title')}
            value={duration(d.alerts.median_s)}
            none={t('dora.alerts.none')}
            detail={t('dora.alerts.detail', { count: d.alerts.resolved })}
          />
        </div>
      </section>

      <section className="space-y-3" aria-labelledby="pipelines-title">
        <div>
          <h2 id="pipelines-title" className="text-lg font-semibold">
            {t('pipelines.title')}
          </h2>
          <p className="text-muted-foreground text-sm">{t('pipelines.description')}</p>
        </div>
        <div className="grid gap-3 sm:grid-cols-3">
          <MetricCard testId="metric-runs" title={t('pipelines.runs')} value={fmt.number(p.summary.runs)} none="" />
          <MetricCard
            testId="metric-success"
            title={t('pipelines.successRate')}
            value={percent(p.summary.success_rate)}
            none={t('pipelines.noDuration')}
            detail={t('pipelines.successDetail', { succeeded: p.summary.succeeded, failed: p.summary.failed, canceled: p.summary.canceled })}
          />
          <MetricCard
            testId="metric-duration"
            title={t('pipelines.duration')}
            value={duration(p.summary.duration_p50_s)}
            none={t('pipelines.noDuration')}
            detail={p.summary.duration_p95_s === null ? undefined : t('pipelines.durationDetail', { value: duration(p.summary.duration_p95_s) })}
          />
        </div>
        <div className="grid gap-3 lg:grid-cols-2">
          <Card className="p-4">
            <StackedBars
              title={t('pipelines.chartRuns')}
              summary={t('pipelines.runsSummary', { runs: p.summary.runs, succeeded: p.summary.succeeded, failed: p.summary.failed })}
              series={[
                { label: t('pipelines.chartSucceeded'), tone: 'fill-success bg-success' },
                { label: t('pipelines.chartFailed'), tone: 'fill-destructive bg-destructive' },
                { label: t('pipelines.chartOther'), tone: 'fill-muted-foreground/40 bg-muted-foreground/40' },
              ]}
              periods={p.trend.map((x) => ({ period: x.period, values: [x.succeeded, x.failed, Math.max(0, x.runs - x.succeeded - x.failed)] }))}
            />
          </Card>
          <Card className="p-4">
            <LineChart
              title={t('pipelines.chartDuration')}
              summary={t('pipelines.durationSummary', { value: duration(p.summary.duration_p50_s) ?? '—' })}
              points={p.trend.map((x) => ({ t: Date.parse(`${x.period}T12:00:00Z`), avg: x.duration_p50_s, max: x.duration_p95_s }))}
              from={Date.parse(p.range.from)}
              to={Date.parse(p.range.to)}
              avgLabel={t('pipelines.median')}
              maxLabel={t('pipelines.p95')}
              max={maxDuration}
              unit={` ${t('secondsUnit')}`}
              tone="text-violet-600 dark:text-violet-400"
            />
          </Card>
        </div>
      </section>

      <section className="space-y-3" aria-labelledby="projects-title">
        <h2 id="projects-title" className="text-lg font-semibold">
          {t('projects.title')}
        </h2>
        {nothing ? (
          <EmptyState title={t('empty')} description={t('emptyHint')} />
        ) : (
          <Card className="gap-0 overflow-x-auto p-0">
            <Table data-testid="dashboard-projects">
              <TableHeader>
                <TableRow>
                  <TableHead>{t('projects.project')}</TableHead>
                  <TableHead className="text-end">{t('projects.runs')}</TableHead>
                  <TableHead className="text-end">{t('projects.successRate')}</TableHead>
                  <TableHead className="text-end">{t('projects.duration')}</TableHead>
                  <TableHead className="text-end">{t('projects.changes')}</TableHead>
                  <TableHead className="text-end">{t('projects.failureRate')}</TableHead>
                  <TableHead className="text-end">{t('projects.leadTime')}</TableHead>
                  <TableHead>{t('projects.lastActivity')}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {rows.map((r) => (
                  <TableRow key={r.id} data-testid="dashboard-project-row">
                    <TableCell className="max-w-56 whitespace-normal">
                      <Link to={`/o/${orgSlug}/projects/${r.id}`} className="font-medium break-words hover:underline">
                        {r.name}
                      </Link>
                      {r.lastStatus && <span className="text-muted-foreground block text-xs">{t(`pipeline:status.${r.lastStatus}`)}</span>}
                    </TableCell>
                    <TableCell className="text-end tabular-nums">{fmt.number(r.runs)}</TableCell>
                    <TableCell className="text-end tabular-nums">{percent(r.successRate) ?? '—'}</TableCell>
                    <TableCell className="text-end tabular-nums">{duration(r.durationP50) ?? '—'}</TableCell>
                    <TableCell className="text-end tabular-nums">{fmt.number(r.changes)}</TableCell>
                    <TableCell className="text-end tabular-nums">{percent(r.changeFailureRate) ?? '—'}</TableCell>
                    <TableCell className="text-end tabular-nums">{duration(r.leadTime) ?? '—'}</TableCell>
                    <TableCell className="text-muted-foreground text-sm">{r.lastActivity ? fmt.relative(r.lastActivity) : '—'}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </Card>
        )}
      </section>
    </div>
  );
}
