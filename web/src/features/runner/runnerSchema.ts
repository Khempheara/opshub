import { z } from 'zod';

/** Runner labels as typed in a comma-separated field (mirrors the API's label rule). */
export const LABEL_PATTERN = /^[a-z0-9][a-z0-9._-]{0,62}$/;

export function parseLabels(text: string): string[] {
  const out: string[] = [];
  for (const raw of text.split(',')) {
    const l = raw.trim().toLowerCase();
    if (l && !out.includes(l)) out.push(l);
  }
  return out;
}

const labelsField = z
  .string()
  .refine((v) => parseLabels(v).every((l) => LABEL_PATTERN.test(l)), 'runner:form.labelsInvalid')
  .refine((v) => parseLabels(v).length <= 20, 'runner:form.labelsTooMany');

export const registerSchema = z.object({ labels: labelsField });
export type RegisterValues = z.infer<typeof registerSchema>;

export const editSchema = z.object({
  name: z.string().trim().min(1, 'errors:rules.required').max(100, 'errors:rules.max'),
  labels: labelsField,
  maxConcurrency: z.string().refine((v) => /^\d+$/.test(v.trim()) && Number(v) >= 1 && Number(v) <= 64, 'runner:form.concurrencyRange'),
});
export type EditValues = z.infer<typeof editSchema>;
