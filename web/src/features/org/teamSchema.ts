import { z } from 'zod';

export const teamSchema = z.object({
  name: z.string().trim().min(1, 'errors:rules.required').max(100, 'errors:rules.max'),
  description: z.string().trim().max(500, 'errors:rules.max'),
});
export type TeamValues = z.infer<typeof teamSchema>;
