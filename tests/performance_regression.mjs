// Relative release comparison on the same disposable host, with alternating trials.
import assert from 'node:assert/strict';
import { randomUUID } from 'node:crypto';
import { performance } from 'node:perf_hooks';
import { localTestURL } from './local_fixture.mjs';

const candidate = localTestURL();
const baseline = process.env.DUPABASE_TEST_BASELINE_URL;
assert.ok(baseline && ['127.0.0.1', 'localhost', '[::1]'].includes(new URL(baseline).hostname));
assert.notEqual(candidate, baseline);
const credentials = { email: process.env.DUPABASE_TEST_ADMIN_EMAIL, password: process.env.DUPABASE_TEST_ADMIN_PASSWORD };
const concurrency = 8, requests = 2400, trials = 5;
const states = [];
let clientGroup = 0;
async function setup(base) {
  const post = async (path, body, token) => {
    const r = await fetch(base + path, { method: 'POST', headers: { 'Content-Type': 'application/json', ...(token ? { Authorization: `Bearer ${token}` } : {}) }, body: JSON.stringify(body) });
    const data = await r.json();
    assert.ok(r.ok, `${path}: ${data.error ?? r.status}`);
    return data;
  };
  const token = (await post('/platform/auth/login', credentials)).token;
  const project = await post('/platform/projects', { name: `perf-${randomUUID().slice(0, 8)}` }, token);
  const state = { base, token, project };
  states.push(state);
  await post(`/platform/projects/${project.id}/sql`, { query: 'CREATE TABLE public.performance_rows (id int PRIMARY KEY, body text NOT NULL)' }, token);
  await post(`/platform/projects/${project.id}/sql`, { query: "INSERT INTO public.performance_rows SELECT value, repeat('p',80) FROM generate_series(1,100) value" }, token);
  return state;
}
const quantile = (values, p) => [...values].sort((a, b) => a - b)[Math.floor((values.length - 1) * p)];
async function measure(state, write, count = requests) {
  const latencies = [];
  const start = performance.now();
  // Each IP stays below the normal 60-request burst. Multiple equal bursts
  // make trials long enough to avoid measuring only a startup/scheduler spike.
  assert.equal(count % concurrency, 0);
  let completedPerClient = 0;
  while (completedPerClient < count / concurrency) {
    const group = ++clientGroup;
    const perClient = Math.min(30, count / concurrency - completedPerClient);
    const offset = completedPerClient;
    await Promise.all(Array.from({ length: concurrency }, async (_, worker) => {
    for (let job = 0; job < perClient; job++) {
      const n = (offset + job) * concurrency + worker;
      const before = performance.now();
      const path = write ? `?id=eq.${n % 100 + 1}` : '?select=id,body&order=id&limit=10';
      const r = await fetch(`${state.base}/rest/v1/performance_rows${path}`, {
        method: write ? 'PATCH' : 'GET', headers: { apikey: state.project.service_role_key,
          'X-Forwarded-For': `198.18.${group}.${worker + 1}`,
          ...(write ? { 'Content-Type': 'application/json' } : {}) },
        body: write ? JSON.stringify({ body: 'p'.repeat(80) }) : undefined,
      });
      const bytes = await r.text();
      assert.ok(r.ok, `Benchmark request failed ${r.status}: ${bytes}`);
      if (!write) assert.equal(JSON.parse(bytes).length, 10, 'Benchmark retains the response contract');
      latencies.push(performance.now() - before);
    }
    }));
    completedPerClient += perClient;
  }
  const seconds = (performance.now() - start) / 1000;
  return { requests: count, seconds, requestsPerSecond: count / seconds, p50Ms: quantile(latencies, 0.5), p95Ms: quantile(latencies, 0.95) };
}
try {
  const previous = await setup(baseline), current = await setup(candidate);
  const report = { baseline: process.env.DUPABASE_TEST_BASELINE_VERSION ?? 'v1.0.0', concurrency, requestsPerTrial: requests, trials,
    clientModel: '8 concurrent clients per burst behind an explicitly trusted disposable gateway; at most 30 requests per address; default rate limits', workloads: {} };
  for (const [name, write] of [['read', false], ['filtered-write', true]]) {
    await measure(previous, write, 80);
    await measure(current, write, 80);
    const before = [], after = [];
    for (let n = 0; n < trials; n++) {
      if (n % 2) { after.push(await measure(current, write)); before.push(await measure(previous, write)); }
      else { before.push(await measure(previous, write)); after.push(await measure(current, write)); }
    }
    const median = results => ({ requestsPerSecond: quantile(results.map(r => r.requestsPerSecond), 0.5), p95Ms: quantile(results.map(r => r.p95Ms), 0.5) });
    const old = median(before), updated = median(after);
    const throughputRatio = updated.requestsPerSecond / old.requestsPerSecond;
    const latencyRatio = updated.p95Ms / old.p95Ms;
    report.workloads[name] = { baseline: old, candidate: updated, throughputRatio, latencyRatio, baselineTrials: before, candidateTrials: after };
  }
  console.log(JSON.stringify(report));
  for (const [name, value] of Object.entries(report.workloads)) {
    assert.ok(value.throughputRatio >= 0.75, `${name}: throughput regressed by more than 25%`);
    assert.ok(value.latencyRatio <= 1.5, `${name}: p95 latency increased by more than 50%`);
  }
} finally {
  for (const state of states) {
    const response = await fetch(`${state.base}/platform/projects/${state.project.id}`, { method: 'DELETE', headers: { Authorization: `Bearer ${state.token}` } });
    assert.ok(response.ok, 'Performance fixture cleanup');
  }
}
