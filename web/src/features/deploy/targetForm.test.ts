import { describe, expect, it } from 'vitest';
import { ApiError } from '@/lib/api/fetcher';
import type { DeployTarget } from '@/lib/api/generated/model';
import { emptyForm, fieldErrors, fromTarget, targetSummary, toConfig, toCredentials, type TargetForm } from './targetForm';

const form = (over: Partial<TargetForm>): TargetForm => ({ ...emptyForm, ...over });

describe('toConfig', () => {
  it('builds an SSH config and keeps pinned host keys', () => {
    const c = toConfig(
      form({ kind: 'ssh', hosts: ' web-1 \n\n10.0.0.5:2222 ', user: ' deploy ', command: './deploy.sh', batchSize: '2', healthUrl: 'http://{host}/healthz', healthTimeout: '30' }),
      { host_keys: { 'web-1:22': 'SHA256:x' } },
    );
    expect(c).toEqual({
      hosts: ['web-1', '10.0.0.5:2222'], user: 'deploy', command: './deploy.sh', batch_size: 2,
      host_keys: { 'web-1:22': 'SHA256:x' }, health_check: { url: 'http://{host}/healthz', timeout_seconds: 30 },
    });
  });
  it('builds a Docker config per connection', () => {
    const c = toConfig(form({ kind: 'docker', connection: 'tls', host: 'docker:2376', container: 'web', replicas: '1', ports: '80:8080', env: 'A=1\nB=x=y' }));
    expect(c).toMatchObject({ connection: 'tls', host: 'docker:2376', user: undefined, socket: undefined, ports: ['80:8080'], env: { A: '1', B: 'x=y' } });
    expect(toConfig(form({ kind: 'docker', connection: 'local', container: 'web' }))).toMatchObject({ host: undefined });
  });
  it('builds a Kubernetes config without a health check when no URL is given', () => {
    expect(toConfig(form({ kind: 'kubernetes', namespace: 'prod', deployment: 'web', service: 'web' }))).toEqual({
      namespace: 'prod', deployment: 'web', container: undefined, service: 'web', rollout_timeout_seconds: 300, health_check: undefined,
    });
  });
  it('keeps non-numbers for the server to reject', () => {
    expect(toConfig(form({ kind: 'ssh', batchSize: 'many' })).batch_size).toBeNaN();
  });
});

describe('toCredentials', () => {
  it('returns undefined when every secret is empty (keep the stored ones)', () => {
    expect(toCredentials(form({ kind: 'ssh' }))).toBeUndefined();
    expect(toCredentials(form({ kind: 'kubernetes' }))).toBeUndefined();
  });
  it('sends only the fields of the chosen method', () => {
    expect(toCredentials(form({ kind: 'ssh', authMethod: 'password', password: 'pw', privateKey: 'ignored' }))).toEqual({ password: 'pw' });
    expect(toCredentials(form({ kind: 'docker', connection: 'ssh', privateKey: 'KEY', registryUsername: 'u', registryPassword: 'p' }))).toEqual({
      private_key: 'KEY', registry_username: 'u', registry_password: 'p',
    });
  });
});

describe('fromTarget and targetSummary', () => {
  const target = (kind: DeployTarget['kind'], config: Record<string, unknown>, credentials: string[] = []): DeployTarget => ({
    id: '1', name: 'web', kind, description: '', config: config as unknown as DeployTarget['config'], credentials,
    last_test_at: null, last_test_ok: null, version: 1, created_at: '', updated_at: '',
  });
  it('round-trips an SSH target and never fills secrets', () => {
    const f = fromTarget(target('ssh', { hosts: ['a:22', 'b:22'], user: 'ops', command: 'x', batch_size: 2, health_check: { url: 'http://{host}/', timeout_seconds: 10 } }, ['password']));
    expect(f).toMatchObject({ hosts: 'a:22\nb:22', user: 'ops', batchSize: '2', healthUrl: 'http://{host}/', healthTimeout: '10', authMethod: 'password', password: '' });
    expect(targetSummary(target('ssh', { hosts: ['a:22', 'b:22'] }))).toBe('a:22, b:22');
  });
  it('summarizes Docker and Kubernetes targets', () => {
    expect(targetSummary(target('docker', { connection: 'ssh', host: 'h:22', container: 'web' }))).toBe('web @ h:22');
    expect(targetSummary(target('docker', { connection: 'local', container: 'web' }))).toBe('web @ local');
    expect(targetSummary(target('kubernetes', { namespace: 'prod', deployment: 'api' }))).toBe('prod/api');
  });
});

describe('fieldErrors', () => {
  it('maps server field paths onto form fields', () => {
    const err = new ApiError(422, 'VALIDATION_FAILED', 'x', {
      fields: [
        { field: 'config.hosts[1]', rule: 'host' },
        { field: 'credentials.private_key', rule: 'private_key' },
        { field: 'config.health_check.url', rule: 'url' },
      ],
    });
    const { fields, unmatched } = fieldErrors(err);
    expect(fields.hosts?.rule).toBe('host');
    expect(fields.privateKey?.rule).toBe('private_key');
    expect(fields.healthUrl?.rule).toBe('url');
    expect(unmatched).toBe(false);
  });
  it('reports errors it can’t place', () => {
    expect(fieldErrors(new ApiError(422, 'VALIDATION_FAILED', 'x', { fields: [{ field: 'config.bogus', rule: 'type' }] })).unmatched).toBe(true);
    expect(fieldErrors(new ApiError(409, 'DEPLOY_TARGET_NAME_TAKEN', 'x')).unmatched).toBe(true);
    expect(fieldErrors(null).unmatched).toBe(false);
  });
});
