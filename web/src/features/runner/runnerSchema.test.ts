import { describe, expect, it } from 'vitest';
import { editSchema, parseLabels, registerSchema } from './runnerSchema';

describe('parseLabels', () => {
  it('splits, trims, lowercases and de-duplicates', () => {
    expect(parseLabels(' Linux, docker ,,linux, GPU ')).toEqual(['linux', 'docker', 'gpu']);
    expect(parseLabels('')).toEqual([]);
  });
});

describe('registerSchema', () => {
  it('accepts valid labels and none', () => {
    expect(registerSchema.safeParse({ labels: 'linux, arm64, gpu.large' }).success).toBe(true);
    expect(registerSchema.safeParse({ labels: '' }).success).toBe(true);
  });
  it('rejects labels the API would reject', () => {
    const bad = registerSchema.safeParse({ labels: 'linux, bad label!' });
    expect(bad.success).toBe(false);
    expect(bad.error?.issues[0]?.message).toBe('runner:form.labelsInvalid');
    const many = Array.from({ length: 21 }, (_, i) => `l${String(i)}`).join(',');
    expect(registerSchema.safeParse({ labels: many }).error?.issues[0]?.message).toBe('runner:form.labelsTooMany');
  });
});

describe('editSchema', () => {
  const ok = { name: 'build-1', labels: 'linux', maxConcurrency: '4' };
  it('accepts a valid runner', () => {
    expect(editSchema.safeParse(ok).success).toBe(true);
  });
  it.each(['0', '65', '1.5', 'x', ''])('rejects max concurrency %s', (v) => {
    expect(editSchema.safeParse({ ...ok, maxConcurrency: v }).success).toBe(false);
  });
  it('requires a name', () => {
    expect(editSchema.safeParse({ ...ok, name: '  ' }).error?.issues[0]?.message).toBe('errors:rules.required');
  });
});
