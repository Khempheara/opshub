import { useId } from 'react';
import { useFormat } from '@/i18n/useFormat';
import { cn } from '@/lib/utils';
import { niceMax } from './dashboardView';

export interface BarSeries {
  label: string;
  /** Tailwind fill and background classes, e.g. "fill-success bg-success". */
  tone: string;
}

export interface BarPeriod {
  /** YYYY-MM-DD: the day, or the Monday of the week. */
  period: string;
  /** One value per series, stacked bottom-up. */
  values: number[];
}

const W = 600;
const H = 150;
const PAD = { l: 30, r: 4, t: 6, b: 20 };

/** Noon UTC of a period, so its date reads the same in every time zone. */
const dayOf = (period: string) => Date.parse(`${period}T12:00:00Z`);

/**
 * Stacked bars per day or week, with a legend. Each bar has a tooltip; screen readers get
 * the summary.
 */
export function StackedBars({ title, summary, series, periods }: { title: string; summary: string; series: BarSeries[]; periods: BarPeriod[] }) {
  const fmt = useFormat();
  const id = useId();
  const totals = periods.map((p) => p.values.reduce((a, b) => a + b, 0));
  const max = niceMax(Math.max(0, ...totals));
  const plotW = W - PAD.l - PAD.r;
  const plotH = H - PAD.t - PAD.b;
  const slot = plotW / Math.max(1, periods.length);
  const bar = Math.max(1, Math.min(24, slot * 0.7));
  const y = (v: number) => PAD.t + plotH - (v / max) * plotH;
  const date = (period: string) => fmt.dateTime(dayOf(period), { month: 'short', day: 'numeric', timeZone: 'UTC' });
  const labels = periods.length > 0 ? [...new Set([0, Math.floor((periods.length - 1) / 2), periods.length - 1])] : [];

  return (
    <figure className="min-w-0 space-y-2" aria-labelledby={`${id}-title`}>
      <figcaption id={`${id}-title`} className="text-sm font-medium">
        {title}
      </figcaption>
      <svg viewBox={`0 0 ${W} ${H}`} className="h-auto w-full" role="img" aria-label={`${title}: ${summary}`}>
        {[0, max / 2, max].map((v) => (
          <g key={v}>
            <line x1={PAD.l} x2={W - PAD.r} y1={y(v)} y2={y(v)} className="stroke-border" strokeWidth={1} />
            <text x={PAD.l - 4} y={y(v) + 3} textAnchor="end" className="fill-muted-foreground text-[10px]">
              {fmt.number(v)}
            </text>
          </g>
        ))}
        {periods.map((p, i) => {
          const x = PAD.l + i * slot + (slot - bar) / 2;
          let base = 0;
          return (
            <g key={p.period}>
              <title>{`${date(p.period)}: ${series.map((s, j) => `${s.label} ${fmt.number(p.values[j] ?? 0)}`).join(', ')}`}</title>
              <rect x={PAD.l + i * slot} y={PAD.t} width={slot} height={plotH} className="fill-transparent" />
              {series.map((s, j) => {
                const v = p.values[j] ?? 0;
                if (v <= 0) return null;
                const top = y(base + v);
                const h = y(base) - top;
                base += v;
                return <rect key={s.label} x={x} y={top} width={bar} height={h} rx={1} className={s.tone.split(' ')[0]} />;
              })}
            </g>
          );
        })}
        {labels.map((i) => {
          const p = periods[i];
          if (!p) return null;
          return (
            <text key={p.period} x={PAD.l + i * slot + slot / 2} y={H - 4} textAnchor="middle" className="fill-muted-foreground text-[10px]">
              {date(p.period)}
            </text>
          );
        })}
      </svg>
      <ul className="text-muted-foreground flex flex-wrap gap-x-4 gap-y-1 text-xs">
        {series.map((s) => (
          <li key={s.label} className="inline-flex items-center gap-1.5">
            <span aria-hidden className={cn('size-2.5 rounded-sm', s.tone.split(' ')[1])} />
            {s.label}
          </li>
        ))}
      </ul>
    </figure>
  );
}
