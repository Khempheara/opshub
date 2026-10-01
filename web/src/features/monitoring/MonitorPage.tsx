import { useQueryClient } from "@tanstack/react-query";
import { ArrowLeft, Pause, Pencil, Play, Trash2 } from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link, useNavigate, useParams } from "react-router";
import { toast } from "sonner";
import { useCurrentOrg } from "@/app/org";
import { OrgAction, usePermissions } from "@/app/permissions";
import { ConfirmDialog } from "@/components/common/ConfirmDialog";
import { ErrorState, LoadingState } from "@/components/common/States";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { LineChart } from "@/features/infra/LineChart";
import { useFormat } from "@/i18n/useFormat";
import { errorMessage, hasCode } from "@/lib/api/errors";
import type { Monitor } from "@/lib/api/generated/model";
import {
  deleteMonitor,
  getGetMonitorQueryKey,
  getListMonitorsQueryKey,
  updateMonitor,
  useGetMonitor,
  useGetMonitorResults,
} from "@/lib/api/generated/monitoring/monitoring";
import { ifMatch } from "@/lib/api/idempotency";
import { NotFoundPage } from "@/pages/NotFoundPage";
import { monitorForm, monitorRequest, niceMax } from "./forms";
import { MonitorDialog } from "./MonitorDialog";
import { MonitorStatusLabel, UptimeStrip } from "./status";

const HOUR = 3600 * 1000;
const RANGES = [
  { key: "1h", ms: HOUR },
  { key: "24h", ms: 24 * HOUR },
  { key: "7d", ms: 7 * 24 * HOUR },
  { key: "30d", ms: 30 * 24 * HOUR },
  { key: "90d", ms: 90 * 24 * HOUR },
] as const;
type RangeKey = (typeof RANGES)[number]["key"];

function History({ monitor }: { monitor: Monitor }) {
  const { t } = useTranslation("monitoring");
  const fmt = useFormat();
  const [range, setRange] = useState<RangeKey>("24h");
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = window.setInterval(() => {
      setNow(Date.now());
    }, 30_000);
    return () => {
      window.clearInterval(id);
    };
  }, []);
  const ms = RANGES.find((r) => r.key === range)?.ms ?? 24 * HOUR;
  // Whole minutes keep the query key stable; rounding "to" up keeps the current minute.
  const to = Math.ceil(now / 60_000) * 60_000;
  const from = to - ms;
  const res = useGetMonitorResults(
    monitor.id,
    { from: new Date(from).toISOString(), to: new Date(to).toISOString() },
    { query: { placeholderData: (p) => p } },
  );

  const points = res.data?.points ?? [];
  const latencies = points
    .map((p) => p.latency_max)
    .filter((v): v is number => v !== null);
  const avgs = points
    .map((p) => p.latency_avg)
    .filter((v): v is number => v !== null);
  const summary = t("monitors.charts.summary", {
    avg: fmt.number(
      avgs.reduce((a, b) => a + b, 0) / Math.max(avgs.length, 1),
      { maximumFractionDigits: 0 },
    ),
    max: fmt.number(Math.max(...latencies, 0)),
  });

  return (
    <>
      <Card>
        <CardHeader className="flex flex-row flex-wrap items-center justify-between gap-2">
          <CardTitle>
            {t("monitors.charts.uptime")}
            {res.data?.uptime != null && (
              <span
                className="text-muted-foreground ms-2 text-sm font-normal"
                data-testid="monitor-uptime"
              >
                {t("monitors.charts.uptimeValue", {
                  value: fmt.number(res.data.uptime, {
                    maximumFractionDigits: 2,
                  }),
                })}
              </span>
            )}
          </CardTitle>
          <div
            role="group"
            aria-label={t("monitors.charts.range")}
            className="flex flex-wrap gap-1"
          >
            {RANGES.map((r) => (
              <Button
                key={r.key}
                size="sm"
                variant={range === r.key ? "default" : "outline"}
                aria-pressed={range === r.key}
                onClick={() => {
                  setRange(r.key);
                  setNow(Date.now());
                }}
              >
                {t(`ranges.${r.key}`)}
              </Button>
            ))}
          </div>
        </CardHeader>
        <CardContent className="space-y-6">
          {res.isPending ? (
            <LoadingState rows={2} />
          ) : res.isError ? (
            <ErrorState error={res.error} onRetry={() => void res.refetch()} />
          ) : points.length === 0 ? (
            <p className="text-muted-foreground text-sm">
              {t("monitors.charts.noData")}
            </p>
          ) : (
            <>
              <UptimeStrip
                points={points}
                from={Date.parse(res.data.from)}
                to={Date.parse(res.data.to)}
                stepSeconds={res.data.step_seconds}
              />
              {/* Every check failed: there is no response time to draw, only an empty axis. */}
              {avgs.length === 0 ? (
                <div
                  className="space-y-1 text-sm"
                  data-testid="no-response-time"
                >
                  <p className="font-medium">{t("monitors.charts.latency")}</p>
                  <p className="text-muted-foreground">
                    {t("monitors.charts.noResponses")}
                  </p>
                </div>
              ) : (
                <LineChart
                  title={t("monitors.charts.latency")}
                  points={points.map((p) => ({
                    t: Date.parse(p.t),
                    avg: p.latency_avg,
                    max: p.latency_max,
                  }))}
                  from={from}
                  to={to}
                  summary={summary}
                  max={niceMax(Math.max(...latencies, 1))}
                  unit=" ms"
                  avgLabel={t("monitors.charts.avg")}
                  maxLabel={t("monitors.charts.max")}
                />
              )}
            </>
          )}
        </CardContent>
      </Card>
      {res.data && res.data.recent.length > 0 && (
        <Card className="gap-0 overflow-x-auto p-0">
          <CardHeader className="py-4">
            <CardTitle>{t("monitors.checks.recent")}</CardTitle>
          </CardHeader>
          <Table data-testid="monitor-checks">
            <TableHeader>
              <TableRow>
                <TableHead>{t("monitors.checks.at")}</TableHead>
                <TableHead>{t("monitors.checks.result")}</TableHead>
                <TableHead>{t("monitors.checks.latency")}</TableHead>
                {monitor.kind === "http" && (
                  <TableHead>{t("monitors.checks.code")}</TableHead>
                )}
              </TableRow>
            </TableHeader>
            <TableBody>
              {res.data.recent.map((c) => (
                <TableRow key={c.at}>
                  <TableCell className="text-xs whitespace-nowrap">
                    {fmt.dateTime(c.at, {
                      dateStyle: "short",
                      timeStyle: "medium",
                    })}
                  </TableCell>
                  <TableCell className="whitespace-normal">
                    <MonitorStatusLabel status={c.up ? "up" : "down"} />
                    {c.error && (
                      <span className="text-muted-foreground block text-xs break-words">
                        {c.error}
                      </span>
                    )}
                  </TableCell>
                  <TableCell className="tabular-nums">
                    {c.latency_ms === null
                      ? "—"
                      : t("monitors.ms", { value: fmt.number(c.latency_ms) })}
                  </TableCell>
                  {monitor.kind === "http" && (
                    <TableCell className="tabular-nums">
                      {c.status_code ?? "—"}
                    </TableCell>
                  )}
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Card>
      )}
    </>
  );
}

/** /o/:orgSlug/monitoring/monitors/:monitorId */
export function MonitorPage() {
  const { t } = useTranslation(["monitoring", "common"]);
  const { monitorId = "" } = useParams();
  const org = useCurrentOrg();
  const perms = usePermissions(org.id);
  const canManage = perms.can(OrgAction.monitorManage);
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const fmt = useFormat();
  const [editing, setEditing] = useState(false);
  const monitor = useGetMonitor(monitorId, {
    query: { refetchInterval: 15_000 },
  });

  if (monitor.isPending) return <LoadingState />;
  if (monitor.isError) {
    if (hasCode(monitor.error, "MONITOR_NOT_FOUND")) return <NotFoundPage />;
    return (
      <ErrorState
        error={monitor.error}
        onRetry={() => void monitor.refetch()}
      />
    );
  }
  const m = monitor.data;
  const back = `/o/${org.slug}/monitoring`;

  const toggle = async () => {
    try {
      await updateMonitor(
        m.id,
        monitorRequest({ ...monitorForm(m), enabled: !m.enabled }, false),
        ifMatch(m.version),
      );
      await queryClient.invalidateQueries({
        queryKey: getGetMonitorQueryKey(m.id),
      });
      await queryClient.invalidateQueries({
        queryKey: getListMonitorsQueryKey(org.id),
      });
      toast.success(m.enabled ? t("monitors.paused") : t("monitors.resumed"));
    } catch (err) {
      toast.error(errorMessage(err));
    }
  };
  const remove = async () => {
    try {
      await deleteMonitor(m.id);
      queryClient.removeQueries({ queryKey: getGetMonitorQueryKey(m.id) });
      await queryClient.invalidateQueries({
        queryKey: getListMonitorsQueryKey(org.id),
      });
      toast.success(t("monitors.deleted"));
      await navigate(back);
    } catch (err) {
      toast.error(errorMessage(err));
    }
  };

  return (
    <div className="space-y-6">
      <Link
        to={back}
        className="text-muted-foreground hover:text-foreground inline-flex items-center gap-1 text-sm"
      >
        <ArrowLeft aria-hidden className="size-4 rtl:rotate-180" />
        {t("monitors.back")}
      </Link>
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div className="min-w-0 space-y-1">
          <h1 className="text-2xl font-semibold tracking-tight break-words">
            {m.name}
          </h1>
          <div className="flex flex-wrap items-center gap-3">
            <MonitorStatusLabel status={m.status} />
            {m.down_since && (
              <span className="text-destructive text-sm">
                {t("monitors.downSince", { time: fmt.relative(m.down_since) })}
              </span>
            )}
          </div>
          {m.status === "down" && m.last_error && (
            <p className="text-muted-foreground text-sm break-words">
              {m.last_error}
            </p>
          )}
        </div>
        {canManage && (
          <div className="flex flex-wrap gap-2">
            <Button variant="outline" onClick={() => void toggle()}>
              {m.enabled ? <Pause aria-hidden /> : <Play aria-hidden />}
              {m.enabled ? t("monitors.pause") : t("monitors.resume")}
            </Button>
            <Button
              variant="outline"
              onClick={() => {
                setEditing(true);
              }}
            >
              <Pencil aria-hidden />
              {t("monitors.edit")}
            </Button>
            <ConfirmDialog
              trigger={
                <Button variant="outline">
                  <Trash2 aria-hidden />
                  {t("common:actions.delete")}
                </Button>
              }
              title={t("monitors.deleteTitle", { name: m.name })}
              description={t("monitors.deleteBody")}
              confirmLabel={t("common:actions.delete")}
              onConfirm={remove}
            />
          </div>
        )}
      </div>

      <Card>
        <CardHeader>
          <CardTitle>{t("monitors.details")}</CardTitle>
        </CardHeader>
        <CardContent className="space-y-2 text-sm">
          <p className="font-mono text-xs break-all" dir="ltr">
            {t(`kinds.${m.kind}`)} · {m.target}
          </p>
          <p className="text-muted-foreground">
            {t("monitors.every", {
              interval: fmt.duration(m.interval_seconds * 1000),
              timeout: fmt.duration(m.timeout_ms),
            })}
          </p>
          {m.labels.length > 0 && (
            <div className="flex flex-wrap gap-1">
              {m.labels.map((l) => (
                <Badge key={l} variant="secondary" className="font-mono">
                  {l}
                </Badge>
              ))}
            </div>
          )}
        </CardContent>
      </Card>

      <History monitor={m} />

      {editing && (
        <MonitorDialog
          orgId={org.id}
          orgSlug={org.slug}
          monitor={m}
          onClose={() => {
            setEditing(false);
          }}
        />
      )}
    </div>
  );
}
