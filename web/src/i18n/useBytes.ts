import { useFormat } from './useFormat';

/** Human-readable size in the user's locale and numerals (KB = 1024 bytes). */
export function useBytes() {
  const fmt = useFormat();
  return (n: number) => {
    const units = ['byte', 'kilobyte', 'megabyte', 'gigabyte'] as const;
    let v = n;
    let i = 0;
    while (v >= 1024 && i < units.length - 1) {
      v /= 1024;
      i++;
    }
    return fmt.number(v, { style: 'unit', unit: units[i], unitDisplay: 'short', maximumFractionDigits: i === 0 ? 0 : 1 });
  };
}
