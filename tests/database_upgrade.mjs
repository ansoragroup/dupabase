// Export each supported major, restore older archives and reject downgrades.
import assert from 'node:assert/strict';
import { createHash, randomUUID } from 'node:crypto';
import { readFileSync, writeFileSync, statSync } from 'node:fs';
import { join } from 'node:path';
import { createClient } from '@supabase/supabase-js';
import { localTestURL } from './local_fixture.mjs';

const base = localTestURL();
const major = Number(process.env.DUPABASE_TEST_POSTGRES_MAJOR);
assert.ok([14, 15, 16, 17, 18].includes(major));
const directory = process.env.DUPABASE_TEST_UPGRADE_DIR;
assert.ok(directory && (statSync(directory).mode & 0o077) === 0, 'Archives require a private fixture directory');
let token, checks = 0;
const projects = [], clients = [];
const check = (value, message) => { assert.ok(value, message); checks++; };
async function api(path, method = 'GET', body) {
  const response = await fetch(base + path, { method, headers: {
    'Content-Type': 'application/json', ...(token ? { Authorization: `Bearer ${token}` } : {}),
  }, body: body === undefined ? undefined : JSON.stringify(body) });
  const data = await response.json();
  assert.ok(response.ok, `${path}: ${data.error ?? response.status}`);
  return data;
}
async function project() {
  const p = await api('/platform/projects', 'POST', { name: `major-upgrade-${randomUUID().slice(0, 8)}` });
  projects.push(p);
  return p;
}
const sql = (p, query) => api(`/platform/projects/${p.id}/sql`, 'POST', { query });
function client(p, key = p.anon_key) {
  const c = createClient(base, key, { auth: { persistSession: false, autoRefreshToken: false, detectSessionInUrl: false } });
  clients.push(c);
  return c;
}
async function restore(p, data) {
  const form = new FormData();
  form.set('file', new Blob([data]), 'upgrade.dump');
  form.set('skip_auth_schema', 'false');
  form.set('disable_triggers', 'false');
  form.set('clean_import', 'true');
  const response = await fetch(`${base}/platform/projects/${p.id}/import`, { method: 'POST', headers: { Authorization: `Bearer ${token}` }, body: form });
  const task = await response.json();
  assert.equal(response.status, 202);
  for (let n = 0; n < 300; n++) {
    const result = await api(`/platform/projects/${p.id}/import/${task.id}`);
    if (result.status !== 'running') return result;
    await new Promise(resolve => setTimeout(resolve, 100));
  }
  assert.fail('Upgrade import timed out');
}
const digest = bytes => createHash('sha256').update(bytes).digest('hex');
try {
  token = (await api('/platform/auth/login', 'POST', {
    email: process.env.DUPABASE_TEST_ADMIN_EMAIL, password: process.env.DUPABASE_TEST_ADMIN_PASSWORD,
  })).token;
  const reject = process.env.DUPABASE_TEST_UPGRADE_MODE === 'reject-downgrade';
  const sourceMajors = reject ? [18] : Array.from({ length: major - 14 }, (_, i) => i + 14);
  for (const source of sourceMajors) {
    const metadata = JSON.parse(readFileSync(join(directory, `pg-${source}.json`)));
    const bytes = readFileSync(join(directory, `pg-${source}.dump`));
    check(digest(bytes) === metadata.sha256, 'Fixture archive digest');
    const p = await project();
    await sql(p, 'CREATE TABLE public.before_import (id int PRIMARY KEY)');
    await sql(p, 'INSERT INTO public.before_import VALUES (42)');
    const result = await restore(p, bytes);
    if (reject) {
      check(result.status === 'failed' && /older PostgreSQL|newer destination/.test(result.error_message), 'Downgrade is rejected before clean import');
      check((await sql(p, 'SELECT id FROM public.before_import')).rows[0][0] === 42, 'Rejected downgrade preserves existing data');
      continue;
    }
    check(result.status === 'completed', `Upgrade ${source} to ${major}: ${result.error_message}`);
    const c = client(p);
    const login = await c.auth.signInWithPassword({ email: metadata.email, password: 'CrossMajorFixturePassword2026!' });
    check(!login.error && login.data.user.id === metadata.userId, 'Imported password hash and auth identity');
    const rows = await c.from('upgrade_rows').select('*').order('id');
    check(!rows.error && rows.data.length === 1 && rows.data[0].body === 'Unicode 文字 & #% inherited', 'Imported data and RLS');
    check(rows.data[0].state === 'ready' && rows.data[0].generated === 2 && rows.data[0].payload.nested === 'preserved', 'Enum, domain, generated column and JSONB');
    const rpc = await c.rpc('upgrade_rpc');
    check(!rpc.error && rpc.data[0].body === rows.data[0].body, 'Imported function contract');
    const anonymous = await client(p).from('upgrade_rows').select('*');
    check(!anonymous.error && anonymous.data.length === 0, 'Imported RLS denies anonymous reads');
    const catalog = await sql(p, "SELECT relrowsecurity FROM pg_class WHERE oid='public.upgrade_rows'::regclass");
    check(catalog.rows[0][0] === true, 'RLS catalog state');
    console.log(`PASS: PostgreSQL ${source} → ${major} archive upgrade`);
  }
  if (!reject) {
    const p = await project(), c = client(p);
    const email = `cross-major-${major}-${randomUUID()}@example.test`;
    const signup = await c.auth.signUp({ email, password: 'CrossMajorFixturePassword2026!' });
    assert.ok(!signup.error && signup.data.user);
    const userId = signup.data.user.id;
    for (const query of [
      "CREATE TYPE public.upgrade_state AS ENUM ('ready','waiting')",
      'CREATE DOMAIN public.upgrade_name AS text CHECK (length(VALUE)>0)',
      `CREATE TABLE public.upgrade_rows (id int PRIMARY KEY, user_id uuid NOT NULL,
        body public.upgrade_name NOT NULL, state public.upgrade_state NOT NULL DEFAULT 'ready',
        generated int GENERATED ALWAYS AS (id * 2) STORED, payload jsonb NOT NULL)`,
      'ALTER TABLE public.upgrade_rows ENABLE ROW LEVEL SECURITY',
      'CREATE POLICY own ON public.upgrade_rows TO authenticated USING (user_id=auth.uid()) WITH CHECK (user_id=auth.uid())',
      'CREATE FUNCTION public.upgrade_rpc() RETURNS TABLE(body text) LANGUAGE sql AS $$ SELECT body::text FROM public.upgrade_rows ORDER BY id $$',
    ]) await sql(p, query);
    const inserted = await client(p, p.service_role_key).from('upgrade_rows').insert([
      { id: 1, user_id: userId, body: 'Unicode 文字 & #% inherited', payload: { nested: 'preserved' } },
      { id: 2, user_id: randomUUID(), body: 'other-user-private', payload: { nested: 'other' } },
    ]);
    assert.ok(!inserted.error, inserted.error?.message);
    const response = await fetch(`${base}/platform/projects/${p.id}/export?format=custom`, { headers: { Authorization: `Bearer ${token}` } });
    assert.ok(response.ok);
    const bytes = Buffer.from(await response.arrayBuffer());
    check(bytes.subarray(0, 5).toString() === 'PGDMP', 'Real PostgreSQL export');
    writeFileSync(join(directory, `pg-${major}.dump`), bytes, { mode: 0o600, flag: 'wx' });
    writeFileSync(join(directory, `pg-${major}.json`), JSON.stringify({ major, email, userId, sha256: digest(bytes) }), { mode: 0o600, flag: 'wx' });
  }
  console.log(JSON.stringify({ postgres: major, mode: reject ? 'downgrade-rejection' : 'forward-upgrade', checks, status: 'passed' }));
} finally {
  for (const c of clients) c.auth.stopAutoRefresh();
  for (const p of projects.reverse()) await api(`/platform/projects/${p.id}`, 'DELETE');
}
