#!/usr/bin/env node
// Fails (exit 1) when any locale is missing a namespace or key that exists in English,
// has an empty value, uses different {{placeholders}}, or has keys English doesn't.
// Run in CI via `npm run i18n:check`.
import { readdirSync, readFileSync } from 'node:fs';
import path from 'node:path';

const root = path.join(import.meta.dirname, '..', 'src', 'locales');
const SOURCE = 'en';

function flatten(obj, prefix = '', out = new Map()) {
  for (const [k, v] of Object.entries(obj)) {
    const key = prefix ? `${prefix}.${k}` : k;
    if (v !== null && typeof v === 'object' && !Array.isArray(v)) flatten(v, key, out);
    else out.set(key, v);
  }
  return out;
}

const placeholders = (s) =>
  typeof s === 'string' ? [...s.matchAll(/\{\{\s*([\w.]+)\s*\}\}/g)].map((m) => m[1]).sort().join(',') : '';

function load(locale) {
  const dir = path.join(root, locale);
  const namespaces = new Map();
  for (const file of readdirSync(dir).filter((f) => f.endsWith('.json'))) {
    const ns = file.replace(/\.json$/, '');
    try {
      namespaces.set(ns, flatten(JSON.parse(readFileSync(path.join(dir, file), 'utf8'))));
    } catch (e) {
      problems.push(`${locale}/${file}: invalid JSON (${e.message})`);
    }
  }
  return namespaces;
}

const problems = [];
const locales = readdirSync(root, { withFileTypes: true }).filter((d) => d.isDirectory()).map((d) => d.name);
const source = load(SOURCE);
let keyCount = 0;

for (const locale of locales.filter((l) => l !== SOURCE)) {
  const target = load(locale);
  for (const [ns, keys] of source) {
    const tKeys = target.get(ns);
    if (!tKeys) {
      problems.push(`${locale}: missing namespace file ${ns}.json`);
      continue;
    }
    for (const [key, value] of keys) {
      keyCount++;
      const where = `${locale}/${ns}.json: ${key}`;
      if (!tKeys.has(key)) problems.push(`${where} — missing`);
      else if (typeof tKeys.get(key) !== 'string' || tKeys.get(key).trim() === '') problems.push(`${where} — empty`);
      else if (placeholders(tKeys.get(key)) !== placeholders(value))
        problems.push(`${where} — placeholders differ (en: {{${placeholders(value)}}}, ${locale}: {{${placeholders(tKeys.get(key))}}})`);
    }
    for (const key of tKeys.keys()) {
      if (!keys.has(key)) problems.push(`${locale}/${ns}.json: ${key} — not in ${SOURCE} (stale key?)`);
    }
  }
  for (const ns of target.keys()) {
    if (!source.has(ns)) problems.push(`${locale}: namespace ${ns}.json has no ${SOURCE} counterpart`);
  }
}

if (problems.length) {
  console.error(`✗ i18n check failed (${problems.length} problem${problems.length > 1 ? 's' : ''}):`);
  for (const p of problems) console.error(`  - ${p}`);
  process.exit(1);
}
console.log(`✓ i18n: ${locales.join(', ')} in sync (${source.size} namespaces, ${keyCount} keys checked)`);
