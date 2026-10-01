// Persist original keys, sessions and data across image upgrade and rollback.
import assert from 'node:assert/strict';
import { readFileSync, writeFileSync } from 'node:fs';
import { randomUUID } from 'node:crypto';
import { createClient } from '@supabase/supabase-js';
import { localTestURL } from './local_fixture.mjs';

const base = localTestURL(), marker = process.env.DUPABASE_TEST_RELEASE_MARKER;
assert.ok(marker, 'Private fixture marker is required');
let state;
async function api(path, method = 'GET', body, token = state?.token) {
  const response = await fetch(base + path, { method, headers: {
    'Content-Type': 'application/json', ...(token ? { Authorization: `Bearer ${token}` } : {}),
  }, body: body === undefined ? undefined : JSON.stringify(body) });
  const data = await response.json();
  assert.ok(response.ok, `${path}: ${data.error ?? response.status}`);
  return data;
}
if (process.env.DUPABASE_TEST_RELEASE_PHASE === 'seed') {
  const login = await api('/platform/auth/login', 'POST', { email: process.env.DUPABASE_TEST_ADMIN_EMAIL, password: process.env.DUPABASE_TEST_ADMIN_PASSWORD });
  const project = await api('/platform/projects', 'POST', { name: `release-upgrade-${randomUUID().slice(0, 8)}` }, login.token);
  state = { token: login.token, project };
  const c = createClient(base, project.anon_key, { auth: { persistSession: false, autoRefreshToken: false, detectSessionInUrl: false } });
  try {
    const signup = await c.auth.signUp({ email: `release-${randomUUID()}@example.test`, password: 'ReleaseUpgradeFixturePassword2026!' });
    assert.ok(!signup.error && signup.data.session);
    state.session = signup.data.session;
    for (const query of [
      'CREATE TABLE public.release_data (id int PRIMARY KEY, body text NOT NULL)',
      "INSERT INTO public.release_data VALUES (1,'preserved across upgrade and rollback')",
      'ALTER TABLE public.release_data ENABLE ROW LEVEL SECURITY',
      'CREATE POLICY authenticated_read ON public.release_data TO authenticated USING (true)',
    ]) await api(`/platform/projects/${project.id}/sql`, 'POST', { query });
    writeFileSync(marker, JSON.stringify(state), { mode: 0o600, flag: 'wx' });
  } finally { c.auth.stopAutoRefresh(); }
} else {
  state = JSON.parse(readFileSync(marker));
  const p = await api(`/platform/projects/${state.project.id}`);
  assert.equal(p.anon_key, state.project.anon_key, 'Original project key survives');
  const response = await fetch(`${base}/rest/v1/release_data`, { headers: { apikey: p.anon_key, Authorization: `Bearer ${state.session.access_token}` } });
  const rows = await response.json();
  assert.ok(response.ok && rows.length === 1 && rows[0].body === 'preserved across upgrade and rollback', 'Original JWT, RLS and data survive');
  const c = createClient(base, p.anon_key, { auth: { persistSession: false, autoRefreshToken: false, detectSessionInUrl: false } });
  try {
    const login = await c.auth.signInWithPassword({ email: state.session.user.email, password: 'ReleaseUpgradeFixturePassword2026!' });
    assert.ok(!login.error && login.data.user.id === state.session.user.id, 'Existing password remains valid');
  } finally { c.auth.stopAutoRefresh(); }
  if (process.env.DUPABASE_TEST_RELEASE_PHASE === 'rollback') await api(`/platform/projects/${p.id}`, 'DELETE');
}
console.log(JSON.stringify({ phase: process.env.DUPABASE_TEST_RELEASE_PHASE, status: 'passed' }));
