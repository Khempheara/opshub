import type { DeployTarget, DeployTargetKind } from '@/lib/api/generated/model';
import { ApiError, type FieldError } from '@/lib/api/fetcher';

/** Flat form state for every target kind; only the current kind's fields are sent. */
export interface TargetForm {
  name: string;
  kind: DeployTargetKind;
  description: string;
  // SSH
  hosts: string;
  user: string;
  command: string;
  batchSize: string;
  // Docker
  connection: 'ssh' | 'tls' | 'local';
  host: string;
  socket: string;
  container: string;
  replicas: string;
  ports: string;
  env: string;
  network: string;
  restart: string;
  // Kubernetes
  namespace: string;
  deployment: string;
  k8sContainer: string;
  service: string;
  rolloutTimeout: string;
  // Health check (all kinds)
  healthUrl: string;
  healthTimeout: string;
  // Credentials (write-only; empty on edit = keep)
  authMethod: 'key' | 'password';
  privateKey: string;
  passphrase: string;
  password: string;
  caCert: string;
  clientCert: string;
  clientKey: string;
  registryServer: string;
  registryUsername: string;
  registryPassword: string;
  kubeconfig: string;
}

export const emptyForm: TargetForm = {
  name: '', kind: 'ssh', description: '',
  hosts: '', user: 'deploy', command: '', batchSize: '1',
  connection: 'ssh', host: '', socket: '', container: '', replicas: '1', ports: '', env: '', network: '', restart: 'unless-stopped',
  namespace: 'default', deployment: '', k8sContainer: '', service: '', rolloutTimeout: '300',
  healthUrl: '', healthTimeout: '60',
  authMethod: 'key', privateKey: '', passphrase: '', password: '', caCert: '', clientCert: '', clientKey: '',
  registryServer: '', registryUsername: '', registryPassword: '', kubeconfig: '',
};

const lines = (s: string) =>
  s
    .split('\n')
    .map((l) => l.trim())
    .filter(Boolean);

const int = (s: string) => {
  const n = Number(s.trim());
  return Number.isInteger(n) ? n : s.trim() === '' ? 0 : NaN;
};

/** KEY=VALUE lines → object (invalid lines keep their text as a key so the server rejects them). */
function parseEnv(s: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const l of lines(s)) {
    const i = l.indexOf('=');
    if (i < 0) out[l] = '';
    else out[l.slice(0, i).trim()] = l.slice(i + 1);
  }
  return out;
}

export type Json = Record<string, unknown>;

// The config is a per-kind union in the API types; the form works on plain objects.
export const configOf = (t: DeployTarget): Json => t.config as unknown as Json;
export const asConfig = (c: Json): DeployTarget['config'] => c as unknown as DeployTarget['config'];

/** The API config for the form's kind. Existing pinned host keys are kept. */
export function toConfig(f: TargetForm, existing?: Json): Json {
  const health = f.healthUrl.trim() ? { url: f.healthUrl.trim(), timeout_seconds: int(f.healthTimeout) } : undefined;
  switch (f.kind) {
    case 'ssh':
      return {
        hosts: lines(f.hosts), user: f.user.trim(), command: f.command, batch_size: int(f.batchSize),
        host_keys: existing?.host_keys ?? undefined, health_check: health,
      };
    case 'docker':
      return {
        connection: f.connection,
        host: f.connection === 'local' ? undefined : f.host.trim(),
        user: f.connection === 'ssh' ? f.user.trim() : undefined,
        host_key: f.connection === 'ssh' ? (existing?.host_key ?? undefined) : undefined,
        socket: f.connection === 'tls' ? undefined : f.socket.trim() || undefined,
        container: f.container.trim(), replicas: int(f.replicas), ports: lines(f.ports), env: parseEnv(f.env),
        network: f.network.trim() || undefined, restart: f.restart, health_check: health,
      };
    case 'kubernetes':
      return {
        namespace: f.namespace.trim(), deployment: f.deployment.trim(), container: f.k8sContainer.trim() || undefined,
        service: f.service.trim() || undefined, rollout_timeout_seconds: int(f.rolloutTimeout), health_check: health,
      };
  }
}

/** The credentials for the form's kind, or undefined when every secret field is empty (keep). */
export function toCredentials(f: TargetForm): Record<string, string> | undefined {
  let c: Record<string, string>;
  switch (f.kind) {
    case 'ssh':
      c = f.authMethod === 'key' ? { private_key: f.privateKey, passphrase: f.passphrase } : { password: f.password };
      break;
    case 'docker':
      c =
        f.connection === 'ssh'
          ? { private_key: f.privateKey, passphrase: f.passphrase }
          : f.connection === 'tls'
            ? { ca_cert: f.caCert, client_cert: f.clientCert, client_key: f.clientKey }
            : {};
      Object.assign(c, { registry_server: f.registryServer.trim(), registry_username: f.registryUsername.trim(), registry_password: f.registryPassword });
      break;
    case 'kubernetes':
      c = { kubeconfig: f.kubeconfig };
      break;
  }
  const set = Object.fromEntries(Object.entries(c).filter(([, v]) => v !== ''));
  return Object.keys(set).length > 0 ? set : undefined;
}

const str = (v: unknown) => (typeof v === 'string' ? v : '');
const num = (v: unknown, d: number) => String(typeof v === 'number' ? v : d);

/** Form state for editing a target (secrets start empty). */
export function fromTarget(t: DeployTarget): TargetForm {
  const c = configOf(t);
  const health = (c.health_check ?? {}) as Json;
  const env = (c.env ?? {}) as Record<string, string>;
  return {
    ...emptyForm,
    name: t.name, kind: t.kind, description: t.description,
    hosts: ((c.hosts ?? []) as string[]).join('\n'), user: str(c.user) || emptyForm.user, command: str(c.command),
    batchSize: num(c.batch_size, 1),
    connection: (str(c.connection) || 'ssh') as TargetForm['connection'], host: str(c.host), socket: str(c.socket),
    container: t.kind === 'docker' ? str(c.container) : '', replicas: num(c.replicas, 1),
    ports: ((c.ports ?? []) as string[]).join('\n'),
    env: Object.entries(env)
      .map(([k, v]) => `${k}=${v}`)
      .join('\n'),
    network: str(c.network), restart: str(c.restart) || 'unless-stopped',
    namespace: str(c.namespace) || 'default', deployment: str(c.deployment),
    k8sContainer: t.kind === 'kubernetes' ? str(c.container) : '', service: str(c.service),
    rolloutTimeout: num(c.rollout_timeout_seconds, 300),
    healthUrl: str(health.url), healthTimeout: num(health.timeout_seconds, 60),
    authMethod: t.credentials.includes('password') ? 'password' : 'key',
  };
}

/** A one-line description of where a target deploys. */
export function targetSummary(t: DeployTarget): string {
  const c = configOf(t);
  switch (t.kind) {
    case 'ssh':
      return ((c.hosts ?? []) as string[]).join(', ');
    case 'docker':
      return `${str(c.container)} @ ${str(c.connection) === 'local' ? 'local' : str(c.host)}`;
    case 'kubernetes':
      return `${str(c.namespace)}/${str(c.deployment)}`;
  }
}

/** Server field errors keyed by the form field that shows them. */
const FIELD_MAP: [RegExp, keyof TargetForm][] = [
  [/^name$/, 'name'],
  [/^config\.hosts/, 'hosts'],
  [/^config\.user$/, 'user'],
  [/^config\.command$/, 'command'],
  [/^config\.batch_size$/, 'batchSize'],
  [/^config\.connection$/, 'connection'],
  [/^config\.host$/, 'host'],
  [/^config\.socket$/, 'socket'],
  [/^config\.container$/, 'container'],
  [/^config\.replicas$/, 'replicas'],
  [/^config\.ports/, 'ports'],
  [/^config\.env/, 'env'],
  [/^config\.network$/, 'network'],
  [/^config\.restart$/, 'restart'],
  [/^config\.namespace$/, 'namespace'],
  [/^config\.deployment$/, 'deployment'],
  [/^config\.service$/, 'service'],
  [/^config\.rollout_timeout_seconds$/, 'rolloutTimeout'],
  [/^config\.health_check\.url$/, 'healthUrl'],
  [/^config\.health_check\.timeout_seconds$/, 'healthTimeout'],
  [/^credentials\.private_key$/, 'privateKey'],
  [/^credentials\.password$/, 'password'],
  [/^credentials\.(ca_cert|client_cert)$/, 'clientCert'],
  [/^credentials\.registry_password$/, 'registryPassword'],
  [/^credentials\.kubeconfig$/, 'kubeconfig'],
];

export function fieldErrors(err: unknown): { fields: Partial<Record<keyof TargetForm, FieldError>>; unmatched: boolean } {
  const fields: Partial<Record<keyof TargetForm, FieldError>> = {};
  let unmatched = false;
  if (!(err instanceof ApiError)) return { fields, unmatched: err !== null && err !== undefined };
  if (err.fieldErrors.length === 0) return { fields, unmatched: true };
  for (const fe of err.fieldErrors) {
    const hit = FIELD_MAP.find(([re]) => re.test(fe.field));
    if (hit) fields[hit[1]] ??= fe;
    else unmatched = true;
  }
  return { fields, unmatched };
}
