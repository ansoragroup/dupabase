// Public SDK contracts only, against explicitly disposable local instances.
import assert from 'node:assert/strict';
import { randomUUID } from 'node:crypto';
import { createRequire } from 'node:module';
import { resolve } from 'node:path';
import { localTestURL } from './local_fixture.mjs';

const sdkRoot = process.env.DUPABASE_TEST_SDK_ROOT ?? process.cwd();
const requireSDK = createRequire(resolve(sdkRoot, 'package.json'));
const { createClient } = requireSDK('@supabase/supabase-js');
const legacy = process.env.DUPABASE_TEST_SDK_MAJOR === '1';
const base = localTestURL();
const email = process.env.DUPABASE_TEST_ADMIN_EMAIL;
const password = process.env.DUPABASE_TEST_ADMIN_PASSWORD;
assert.ok(email && password, 'Disposable admin credentials are required');
let token, project, checks = 0;
const clients = [];
const id = randomUUID().replaceAll('-', '').slice(0, 12);
function check(value, message) { assert.ok(value, message); checks++; }
async function api(path, method = 'GET', body) {
  const response = await fetch(base + path, { method, headers: {
    'Content-Type': 'application/json', ...(token ? { Authorization: `Bearer ${token}` } : {}),
  }, body: body === undefined ? undefined : JSON.stringify(body) });
  const data = await response.json();
  assert.ok(response.ok, `Platform request ${path}: ${data.error ?? response.status}`);
  return data;
}
function client(key) {
  const options = { persistSession: false, autoRefreshToken: false, detectSessionInUrl: false };
  const c = createClient(base, key, legacy ? options : { auth: options });
  clients.push(c);
  return c;
}
try {
  token = (await api('/platform/auth/login', 'POST', { email, password })).token;
  project = await api('/platform/projects', 'POST', { name: `sdk-contract-${id}` });
  check(!!project.anon_key && !!project.service_role_key, 'Existing project key fields');
  for (const query of [
    `CREATE TABLE public.contract_rows (id int PRIMARY KEY, user_id uuid NOT NULL,
      body text NOT NULL, amount numeric(10,2) NOT NULL DEFAULT 12.50,
      payload jsonb NOT NULL DEFAULT '{"nested":{"ok":true}}', nullable text)`,
    'ALTER TABLE public.contract_rows ENABLE ROW LEVEL SECURITY',
    `CREATE POLICY own ON public.contract_rows TO authenticated
      USING (user_id=auth.uid()) WITH CHECK (user_id=auth.uid())`,
    `CREATE FUNCTION public.contract_rpc(input_value int) RETURNS TABLE(output_value int) LANGUAGE sql
      IMMUTABLE AS $$ SELECT input_value + 1 $$`,
  ]) await api(`/platform/projects/${project.id}/sql`, 'POST', { query });
  const anon = client(project.anon_key);
  const service = client(project.service_role_key);
  const signup = await anon.auth.signUp({ email: `sdk-${id}@example.test`, password: 'SDKContractPassword2026!' });
  const user = legacy ? signup.user : signup.data?.user;
  const session = legacy ? signup.session : signup.data?.session;
  check(!signup.error && !!user?.id && !!session?.access_token, `SDK signup: ${signup.error?.message}`);
  const seed = await service.from('contract_rows').insert([
    { id: 1, user_id: user.id, body: 'first & # % Unicode 文字' },
    { id: 2, user_id: user.id, body: 'second' },
    { id: 3, user_id: randomUUID(), body: 'other user' },
  ]).select();
  check(!seed.error && seed.data.length === 3, 'Insert and returning representation');
  const rows = await anon.from('contract_rows').select('*').order('id');
  check(!rows.error && rows.data.length === 2 && rows.data[0].id === 1, 'RLS, select and ordering');
  check(rows.data[0].body === 'first & # % Unicode 文字', 'UTF-8 and reserved characters');
  check(rows.data[0].payload.nested.ok === true && rows.data[0].nullable === null, 'JSONB and SQL NULL');
  check(Number(rows.data[0].amount) === 12.5, 'Numeric representation');
  const single = await anon.from('contract_rows').select('id,body').eq('id', 1).single();
  check(!single.error && single.data.id === 1 && !('amount' in single.data), 'Projection, eq and single');
  const filtered = await anon.from('contract_rows').select('*').in('id', [1, 2]).gt('id', 1);
  check(!filtered.error && filtered.data.length === 1 && filtered.data[0].id === 2, 'IN and range filters');
  const ranged = await anon.from('contract_rows').select('*', { count: 'exact' }).order('id').range(1, 1);
  check(!ranged.error && ranged.data.length === 1 && ranged.data[0].id === 2 && ranged.count === 2, 'Pagination and exact count');
  const update = await anon.from('contract_rows').update({ body: 'updated' }).eq('id', 1).select();
  check(!update.error && update.data[0].body === 'updated', 'Update and representation');
  const upsert = await service.from('contract_rows').upsert({ id: 2, user_id: user.id, body: 'upserted' }, { onConflict: 'id' }).select();
  check(!upsert.error && upsert.data[0].body === 'upserted', 'Upsert preserves onConflict');
  const rpc = await anon.rpc('contract_rpc', { input_value: 41 });
  check(!rpc.error && rpc.data?.[0]?.output_value === 42, `RPC table result: ${rpc.error?.message}`);
  const denied = await anon.from('contract_rows').insert({ id: 4, user_id: randomUUID(), body: 'denied' });
  check(!!denied.error && typeof denied.error.message === 'string', 'RLS rejection and error shape');
  const badPassword = legacy
    ? await client(project.anon_key).auth.signIn({ email: user.email, password: 'IncorrectPassword2026!' })
    : await client(project.anon_key).auth.signInWithPassword({ email: user.email, password: 'IncorrectPassword2026!' });
  check(!!badPassword.error, 'Incorrect password rejected');
  const login = legacy
    ? await anon.auth.signIn({ email: user.email, password: 'SDKContractPassword2026!' })
    : await anon.auth.signInWithPassword({ email: user.email, password: 'SDKContractPassword2026!' });
  const loginSession = legacy ? login.session : login.data?.session;
  check(!login.error && loginSession?.user.id === user.id, 'Password login with existing identity');
  const detail = legacy ? await anon.auth.api.getUser(loginSession.access_token) : await anon.auth.getUser();
  check(!detail.error && (legacy ? detail.user : detail.data?.user)?.id === user.id, 'Get authenticated user');
  const profile = legacy ? await anon.auth.update({ data: { label: 'backward-compatible' } }) : await anon.auth.updateUser({ data: { label: 'backward-compatible' } });
  check(!profile.error && (legacy ? profile.user : profile.data?.user)?.user_metadata.label === 'backward-compatible', 'Update user metadata');
  const refresh = legacy ? await anon.auth.api.refreshAccessToken(loginSession.refresh_token) : await anon.auth.refreshSession();
  const refreshed = legacy ? refresh.data : refresh.data?.session;
  check(!refresh.error && refreshed?.access_token && refreshed.refresh_token !== loginSession.refresh_token, 'Refresh rotates the token');
  if (legacy) check(!(await anon.auth.setSession(refreshed)).error, 'Install refreshed legacy session');
  const removed = await anon.from('contract_rows').delete().eq('id', 1).select();
  check(!removed.error && removed.data.length === 1, 'Delete with a filter');
  const logout = await anon.auth.signOut();
  check(!logout.error, 'Logout preserves the SDK result');
  const afterLogout = await fetch(`${base}/auth/v1/token?grant_type=refresh_token`, {
    method: 'POST', headers: { apikey: project.anon_key, 'Content-Type': 'application/json' },
    body: JSON.stringify({ refresh_token: refreshed.refresh_token }),
  });
  check(!afterLogout.ok, 'Logout invalidates refresh credentials');
  const anonymous = await client(project.anon_key).from('contract_rows').select('*');
  check(!anonymous.error && anonymous.data.length === 0, 'Anonymous RLS after logout');
  console.log(JSON.stringify({ sdk: process.env.DUPABASE_TEST_SDK_VERSION ?? 'locked-current', postgres: process.env.DUPABASE_TEST_POSTGRES_MAJOR, checks, status: 'passed' }));
} finally {
  for (const c of clients) c.auth.stopAutoRefresh?.();
  if (project) await api(`/platform/projects/${project.id}`, 'DELETE');
}
