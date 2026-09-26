// k6 load test for OpsHub (docs/load-testing.md). Run with `make load` against the dev stack
// after `make seed`, or in CI with the "Load test" workflow.
//
// Two scenarios at a constant arrival rate:
//   browse  — the four demo users read what people look at most: organizations, projects, runs,
//             the dashboard, logs, monitors and (Owner/Admin) the audit log
//   ingest  — a service sends batches of 50 log lines with an ingest token
//
// The API limits requests per client IP (3 × OPSHUB_RATE_LIMIT_RPS) and per user or token
// (OPSHUB_RATE_LIMIT_RPS). k6 sends everything from one address, so the default rates stay
// below those limits and the test measures latency, not rate limiting.
//
// Environment: BASE_URL (default http://localhost:3000), SEED_PASSWORD, ORG_SLUG,
// BROWSE_RATE (requests/s, default 30), INGEST_RATE (batches/s, default 5), DURATION (1m).
import http from 'k6/http';
import { check, fail } from 'k6';
import { Counter } from 'k6/metrics';

const BASE = (__ENV.BASE_URL || 'http://localhost:3000').replace(/\/$/, '');
const API = `${BASE}/api/v1`;
const PASSWORD = __ENV.SEED_PASSWORD || 'angkor-wat-sunrise-2026';
const ORG = __ENV.ORG_SLUG || 'angkor-tech';
const USERS = ['owner', 'admin', 'dev', 'viewer'].map((u) => `${u}@demo.opshub.local`);
const DURATION = __ENV.DURATION || '1m';

const rateLimited = new Counter('opshub_rate_limited');

export const options = {
  scenarios: {
    browse: {
      executor: 'constant-arrival-rate',
      exec: 'browse',
      rate: Number(__ENV.BROWSE_RATE || 30),
      timeUnit: '1s',
      duration: DURATION,
      preAllocatedVUs: 20,
      maxVUs: 60,
    },
    ingest: {
      executor: 'constant-arrival-rate',
      exec: 'ingest',
      rate: Number(__ENV.INGEST_RATE || 5),
      timeUnit: '1s',
      duration: DURATION,
      preAllocatedVUs: 5,
      maxVUs: 20,
    },
  },
  thresholds: {
    // Less than 1 % of requests fail (rate limiting counts as a failure too).
    http_req_failed: ['rate<0.01'],
    // Everyday reads answer within 200 ms for 95 % of requests (the API target).
    'http_req_duration{kind:read}': ['p(95)<200'],
    // Aggregations over a month of history get more room.
    'http_req_duration{kind:dashboard}': ['p(95)<500'],
    'http_req_duration{kind:search}': ['p(95)<300'],
    'http_req_duration{kind:ingest}': ['p(95)<300'],
    opshub_rate_limited: ['count==0'],
  },
};

function json(res, what) {
  if (res.status !== 200 && res.status !== 201) fail(`${what}: HTTP ${res.status} ${res.body}`);
  return res.json();
}

function auth(token) {
  return { headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' } };
}

// setup signs in the demo users, finds the organization and its projects, and creates an
// ingest token for the ingest scenario (revoked in teardown).
export function setup() {
  const users = USERS.map((email) => {
    const body = json(http.post(`${API}/auth/login`, JSON.stringify({ email, password: PASSWORD }), { headers: { 'Content-Type': 'application/json' } }), `login ${email}`);
    if (body.status !== 'authenticated') fail(`login ${email}: ${body.status} (two-factor authentication is not supported here)`);
    return { email, token: body.tokens.access_token };
  });
  const admin = users[1].token;
  const org = json(http.get(`${API}/orgs?limit=200`, auth(admin)), 'organizations').items.find((o) => o.slug === ORG);
  if (!org) fail(`organization ${ORG} not found — run \`make seed\``);
  const projects = json(http.get(`${API}/orgs/${org.id}/projects?limit=50`, auth(admin)), 'projects').items.map((p) => p.id);
  const token = json(
    http.post(`${API}/orgs/${org.id}/log-ingest-tokens`, JSON.stringify({ name: `k6 ${Date.now()}`, service: 'k6/load-test' }), auth(admin)),
    'ingest token',
  );
  return { users, orgId: org.id, projects, ingest: { id: token.id, token: token.token }, admin };
}

function get(url, token, kind, name) {
  const res = http.get(url, Object.assign(auth(token), { tags: { kind, name } }));
  if (res.status === 429) rateLimited.add(1);
  check(res, { [`${name} 200`]: (r) => r.status === 200 });
}

// browse: one request per iteration, rotating over users and pages.
export function browse(data) {
  const user = data.users[__ITER % data.users.length];
  const project = data.projects[__ITER % data.projects.length];
  const o = `${API}/orgs/${data.orgId}`;
  const since = new Date(Date.now() - 30 * 86400_000).toISOString();
  const pages = [
    [`${API}/orgs?limit=50`, 'read', 'organizations'],
    [`${o}/projects?limit=50`, 'read', 'projects'],
    [`${API}/projects/${project}/runs?limit=20`, 'read', 'runs'],
    [`${o}/dashboard/dora?from=${since}&tz=Asia/Phnom_Penh`, 'dashboard', 'dashboard dora'],
    [`${o}/dashboard/pipelines?from=${since}&tz=Asia/Phnom_Penh`, 'dashboard', 'dashboard pipelines'],
    [`${o}/logs?q=payment&limit=100`, 'search', 'log search'],
    [`${o}/monitors`, 'read', 'monitors'],
    [`${API}/me`, 'read', 'me'],
  ];
  if (user.email.startsWith('owner') || user.email.startsWith('admin')) pages.push([`${o}/audit-log?limit=50`, 'read', 'audit log']);
  const [url, kind, name] = pages[Math.floor(__ITER / data.users.length) % pages.length];
  get(url, user.token, kind, name);
}

// ingest: a batch of 50 NDJSON lines.
export function ingest(data) {
  const now = Date.now();
  let body = '';
  for (let i = 0; i < 50; i++) {
    body += `${JSON.stringify({ ts: new Date(now - i * 10).toISOString(), level: i % 10 === 0 ? 'error' : 'info', message: `k6 request ${__ITER}-${i} GET /api/cart 200`, ms: i })}\n`;
  }
  const res = http.post(`${API}/ingest/logs`, body, {
    headers: { Authorization: `Bearer ${data.ingest.token}`, 'Content-Type': 'application/x-ndjson' },
    tags: { kind: 'ingest', name: 'ingest' },
  });
  if (res.status === 429) rateLimited.add(1);
  check(res, { 'ingest 200': (r) => r.status === 200, 'ingest accepted all': (r) => r.status === 200 && r.json('accepted') === 50 });
}

export function teardown(data) {
  http.del(`${API}/log-ingest-tokens/${data.ingest.id}`, null, auth(data.admin));
}
