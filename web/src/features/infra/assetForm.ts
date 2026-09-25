import type { Asset, AssetKind, AssetRequest } from '@/lib/api/generated/model';

/** Form state for creating or editing an asset. */
export interface AssetForm {
  kind: AssetKind;
  name: string;
  address: string;
  description: string;
  tags: string;
  metadata: string;
  tlsPort: string;
}

export const emptyAssetForm: AssetForm = { kind: 'server', name: '', address: '', description: '', tags: '', metadata: '', tlsPort: '443' };

/** "a, b  c" → ["a", "b", "c"] (the server lowercases, de-duplicates and validates). */
export function parseTags(s: string): string[] {
  return s
    .split(/[,\s]+/)
    .map((x) => x.trim())
    .filter(Boolean);
}

/** KEY=value lines → object; a line without "=" keeps its text as the key so the server reports it. */
export function parseMetadata(s: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const raw of s.split('\n')) {
    const line = raw.trim();
    if (!line) continue;
    const i = line.indexOf('=');
    if (i < 0) out[line] = '';
    else out[line.slice(0, i).trim()] = line.slice(i + 1).trim();
  }
  return out;
}

export function toRequest(f: AssetForm, create: boolean): AssetRequest {
  const n = Number(f.tlsPort.trim());
  return {
    ...(create ? { kind: f.kind } : {}),
    name: f.name.trim(),
    address: f.address.trim(),
    description: f.description.trim(),
    tags: parseTags(f.tags),
    metadata: parseMetadata(f.metadata),
    tls_port: Number.isInteger(n) && f.tlsPort.trim() !== '' ? n : f.tlsPort.trim() === '' ? 443 : -1,
  };
}

export function fromAsset(a: Asset): AssetForm {
  return {
    kind: a.kind,
    name: a.name,
    address: a.address,
    description: a.description,
    tags: a.tags.join(', '),
    metadata: Object.entries(a.metadata)
      .map(([k, v]) => `${k}=${v}`)
      .join('\n'),
    tlsPort: String(a.tls_port),
  };
}
