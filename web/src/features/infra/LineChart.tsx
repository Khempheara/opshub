import { useId, useMemo, useState } from 'react';
import { useFormat } from '@/i18n/useFormat';
import { cn } from '@/lib/utils';

export interface ChartPoint {
  t: number; // epoch ms
  avg: number | null;
  max: number | null;
}

const W = 600;
const H = 160;
const PAD = { r: 8, t: 8, b: 22 };

/** Splits a series into drawable runs, breaking at missing values. */
function paths(points: ChartPoint[], key: 'avg' | 'max', x: (t: number) => number, y: (v: number) => number): string {
  let d = '';
  let pen = false;
  for (const p of points) {
    const v = p[key];
    if (v === null) {
      pen = false;
      continue;
    }
    d += `${pen ? 'L' : 'M'}${x(p.t).toFixed(1)},${y(v).toFixed(1)}`;
    pen = true;
  }
  return d;
}

/**
 * A line chart from 0 to max (default 0–100 %): the average per step, and the maximum as a lighter line. Gaps are
 * steps without data. Hovering shows the nearest step; screen readers get the summary.
 */
export function LineChart({
  title,
  points,
  from,
  to,
  summary,
  tone = 'text-sky-600 dark:text-sky-400',
  maxLabel,
  avgLabel,
  max = 100,
  unit = '%',
}: {
  title: string;
  points: ChartPoint[];
  from: number;
  to: number;
  summary: string;
  tone?: string;
  avgLabel: string;
  maxLabel: string;
  /** The top of the scale. */
  max?: number;
  /** Appended to values ("%", " ms"). */
  unit?: string;
}) {
  const fmt = useFormat();
  const id = useId();
  const [hover, setHover] = useState<ChartPoint | null>(null);
  const span = Math.max(to - from, 1);
  // Room for the widest y-axis label ("100%", "2,000 ms"), about 6 units per character.
  const padL = Math.max(34, (fmt.number(max) + unit).length * 6 + 8);
  const x = (t: number) => padL + ((t - from) / span) * (W - padL - PAD.r);
  const y = (v: number) => PAD.t + (1 - Math.min(v, max) / max) * (H - PAD.t - PAD.b);
  const { avgPath, maxPath } = useMemo(
    () => ({ avgPath: paths(points, 'avg', x, y), maxPath: paths(points, 'max', x, y) }),
    // eslint-disable-next-line react-hooks/exhaustive-deps -- x and y depend only on from/to
    [points, from, to, max],
  );
  const ticks = [0, 1, 2, 3].map((i) => from + (span * i) / 3);
  const short = span <= 36 * 3600 * 1000;
  const label = (t: number) => fmt.dateTime(t, short ? { timeStyle: 'short' } : { month: 'short', day: 'numeric' });

  const onMove = (e: React.PointerEvent<SVGSVGElement>) => {
    const box = e.currentTarget.getBoundingClientRect();
    const t = from + (((e.clientX - box.left) / box.width) * W - padL) / (W - padL - PAD.r) * span;
    let best: ChartPoint | null = null;
    for (const p of points) if (!best || Math.abs(p.t - t) < Math.abs(best.t - t)) best = p;
    setHover(best);
  };

  return (
    <figure className="space-y-1" aria-labelledby={`${id}-title`}>
      <figcaption className="flex items-baseline justify-between gap-2 text-sm">
        <span id={`${id}-title`} className="font-medium">
          {title}
        </span>
        <span className="text-muted-foreground text-xs" aria-live="polite">
          {hover
            ? `${fmt.dateTime(hover.t, { dateStyle: 'short', timeStyle: 'short' })} · ${avgLabel} ${hover.avg === null ? '—' : fmt.number(hover.avg, { maximumFractionDigits: 1 }) + unit} · ${maxLabel} ${hover.max === null ? '—' : fmt.number(hover.max, { maximumFractionDigits: 1 }) + unit}`
            : summary}
        </span>
      </figcaption>
      <svg
        viewBox={`0 0 ${String(W)} ${String(H)}`}
        className={cn('h-auto w-full touch-none', tone)}
        role="img"
        aria-label={`${title}: ${summary}`}
        onPointerMove={onMove}
        onPointerLeave={() => {
          setHover(null);
        }}
      >
        {[0, max / 2, max].map((v) => (
          <g key={v}>
            <line x1={padL} x2={W - PAD.r} y1={y(v)} y2={y(v)} className="stroke-border" strokeWidth={1} vectorEffect="non-scaling-stroke" />
            <text x={padL - 4} y={y(v) + 3} textAnchor="end" className="fill-muted-foreground text-[10px]">
              {fmt.number(v)}
              {unit}
            </text>
          </g>
        ))}
        {ticks.map((t, i) => (
          <text key={t} x={x(t)} y={H - 6} textAnchor={i === 0 ? 'start' : i === ticks.length - 1 ? 'end' : 'middle'} className="fill-muted-foreground text-[10px]">
            {label(t)}
          </text>
        ))}
        <path d={maxPath} fill="none" stroke="currentColor" strokeOpacity={0.35} strokeWidth={1.5} vectorEffect="non-scaling-stroke" />
        <path d={avgPath} fill="none" stroke="currentColor" strokeWidth={2} vectorEffect="non-scaling-stroke" />
        {hover && hover.avg !== null && <circle cx={x(hover.t)} cy={y(hover.avg)} r={3} className="fill-current" />}
      </svg>
    </figure>
  );
}
