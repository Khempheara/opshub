import type { Role } from '@/lib/api/generated/model';

export const ALL_ROLES: readonly Role[] = ['owner', 'admin', 'developer', 'viewer'];

/** Native <select> styled like the shadcn/ui input. */
export const selectClass =
  'border-input bg-background focus-visible:border-ring focus-visible:ring-ring/50 block min-h-9 rounded-md border px-3 py-1.5 text-sm outline-none focus-visible:ring-[3px] disabled:opacity-60';
