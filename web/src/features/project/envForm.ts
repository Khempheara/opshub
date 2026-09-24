import type { Environment, EnvironmentRequest, Protection } from '@/lib/api/generated/model';

export interface VariableRow {
  key: string;
  value: string;
}

export type ProtectRole = 'owner' | 'admin' | 'developer';

/** Form state of the environment dialog. */
export interface EnvironmentFormValues {
  name: string;
  kind: Environment['kind'];
  variables: VariableRow[];
  protected: boolean;
  requiredApprovals: number;
  branches: string; // one pattern per line
  roles: ProtectRole[];
}

export const PROTECT_ROLES: readonly ProtectRole[] = ['owner', 'admin', 'developer'];

export function emptyEnvironmentForm(): EnvironmentFormValues {
  return { name: '', kind: 'development', variables: [], protected: false, requiredApprovals: 1, branches: '', roles: ['owner', 'admin'] };
}

/** Environment → form values. */
export function toFormValues(e: Environment): EnvironmentFormValues {
  const p = e.protection;
  return {
    name: e.name,
    kind: e.kind,
    variables: Object.entries(e.variables)
      .sort(([a], [b]) => a.localeCompare(b))
      .map(([key, value]) => ({ key, value })),
    protected: Boolean(p),
    requiredApprovals: p?.required_approvals ?? 1,
    branches: (p?.allowed_branches ?? []).join('\n'),
    roles: p?.allowed_roles ?? ['owner', 'admin'],
  };
}

/** Splits the branches textarea into trimmed, de-duplicated patterns. */
export function parseBranches(text: string): string[] {
  const out: string[] = [];
  for (const line of text.split(/[\n,]/)) {
    const b = line.trim();
    if (b && !out.includes(b)) out.push(b);
  }
  return out;
}

/** Form values → API request. Empty variable rows are dropped; the last duplicate key wins. */
export function toRequest(v: EnvironmentFormValues): EnvironmentRequest {
  const variables: Record<string, string> = {};
  for (const row of v.variables) {
    const key = row.key.trim();
    if (key) variables[key] = row.value;
  }
  const protection: Protection | null = v.protected
    ? { required_approvals: v.requiredApprovals, allowed_branches: parseBranches(v.branches), allowed_roles: v.roles }
    : null;
  return { name: v.name.trim(), kind: v.kind, variables, protection };
}
