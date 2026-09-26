import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { StackedBars } from './StackedBars';

const periods = ['2026-08-27', '2026-09-11', '2026-09-26'].map((period, i) => ({ period, values: [i + 1, i] }));
const series = [
  { label: 'Successful', tone: 'fill-success bg-success' },
  { label: 'Failed', tone: 'fill-destructive bg-destructive' },
];

describe('StackedBars', () => {
  it('labels the chart for screen readers and shows a legend', () => {
    render(<StackedBars title="Production changes" summary="6 successful" series={series} periods={periods} />);
    expect(screen.getByRole('img', { name: 'Production changes: 6 successful' })).toBeInTheDocument();
    expect(screen.getByText('Successful')).toBeInTheDocument();
    expect(screen.getByText('Failed')).toBeInTheDocument();
  });

  it('keeps the first and last date labels inside the plot', () => {
    const { container } = render(<StackedBars title="t" summary="s" series={series} periods={periods} />);
    const dates = [...container.querySelectorAll('text')].filter((t) => /\d/.test(t.textContent) && /[A-Za-z]/.test(t.textContent));
    expect(dates.map((t) => t.getAttribute('text-anchor'))).toEqual(['start', 'middle', 'end']);
  });

  it('draws one stacked bar per value above zero, each with a tooltip', () => {
    const { container } = render(<StackedBars title="t" summary="s" series={series} periods={periods} />);
    // 3 hover areas + bars: [1,0] → 1, [2,1] → 2, [3,2] → 2
    expect(container.querySelectorAll('rect')).toHaveLength(3 + 5);
    expect([...container.querySelectorAll('title')].map((t) => t.textContent)[2]).toMatch(/Successful 3, Failed 2$/);
  });
});
