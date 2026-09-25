import type { Environment, Role } from '@/lib/api/generated/model';

/** A secret's name: an environment variable name; OPSHUB_ is reserved (mirrors the API). */
export const SECRET_NAME_PATTERN = /^[A-Z_][A-Z0-9_]{0,127}$/;
export const RESERVED_PREFIX = 'OPSHUB_';
export const MAX_VALUE_BYTES = 64 * 1024;

/** The value of the scope select that means "every environment". */
export const ALL_ENVIRONMENTS = '';

/** Helps typing a name: upper case, spaces and dashes become underscores. */
export function normalizeName(s: string): string {
  return s.toUpperCase().replace(/[\s-]+/g, '_');
}

export type NameProblem = 'secret_name' | 'reserved' | null;

export function nameProblem(name: string): NameProblem {
  if (!SECRET_NAME_PATTERN.test(name)) return 'secret_name';
  if (name.startsWith(RESERVED_PREFIX)) return 'reserved';
  return null;
}

export function valueTooLarge(value: string): boolean {
  return new TextEncoder().encode(value).length > MAX_VALUE_BYTES;
}

/**
 * Where the caller may create a secret (mirrors the API): Admins and Owners anywhere,
 * including every environment at once; Developers only in unprotected environments.
 */
export function creatableScopes(environments: Environment[], role: Role): { all: boolean; environments: Environment[] } {
  const admin = role === 'owner' || role === 'admin';
  return { all: admin, environments: admin ? environments : environments.filter((e) => e.protection === null) };
}

/** The `.opshub.yml` lines that give a job this secret. */
export function usageSnippet(name: string, environment: string | null): string {
  const env = environment ? `\n    environment: ${environment}` : '';
  return `jobs:\n  deploy:${env}\n    secrets: [${name}]`;
}
