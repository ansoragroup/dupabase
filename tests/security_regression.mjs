// Run only against a disposable local instance. No embedded application credentials.
import assert from 'node:assert/strict';
import { randomUUID } from 'node:crypto';
import { createClient } from '@supabase/supabase-js';

const base = process.env.DUPABASE_TEST_URL;
assert.ok(base, 'DUPABASE_TEST_URL must name a disposable local instance');
assert.ok(['127.0.0.1', 'localhost', '[::1]'].includes(new URL(base).hostname), 'Remote security test targets are forbidden');
const email = process.env.DUPABASE_TEST_ADMIN_EMAIL;
const password = process.env.DUPABASE_TEST_ADMIN_PASSWORD;
assert.ok(email && password, 'Set the disposable admin email/password');
const run = randomUUID().replaceAll('-', '').slice(0, 12);
const storageEndpoint = process.env.DUPABASE_TEST_S3_INTERNAL_URL;
if (storageEndpoint) assert.ok(['maintenance-s3','127.0.0.1','localhost'].includes(new URL(storageEndpoint).hostname),'S3 tests require a disposable fixture');
let token;
let checks = 0;
const projects = [];
const clients = [];
const platformUsers = [];
let initialMode;

async function request(path, { method = 'GET', body, auth = token, key, bearer, headers = {} } = {}) {
  const response = await fetch(`${base}${path}`, {
    method,
    headers: { ...(body === undefined ? {} : { 'Content-Type': 'application/json' }), ...(auth ? { Authorization: `Bearer ${auth}` } : {}), ...(key ? { apikey: key } : {}), ...(bearer ? { Authorization: `Bearer ${bearer}` } : {}), ...headers },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  return { status: response.status, data: await response.json() };
}
function check(condition, message) { assert.ok(condition, message); checks++; }
async function sql(project, query, readOnly = false) {
  return request(`/platform/projects/${project.id}/sql`, { method: 'POST', body: { query, read_only: readOnly } });
}
async function createProject(name, settings = {}) {
  const result = await request('/platform/projects', { method: 'POST', body: { name: `${name}-${run}`, ...settings } });
  check(result.status === 201, `project provisioning: ${result.data.error ?? result.status}`);
  projects.push(result.data);
  return result.data;
}
function client(project, key = project.anon_key) {
  const result = createClient(base, key, { auth: { persistSession: false, autoRefreshToken: false, detectSessionInUrl: false } });
  clients.push(result);
  return result;
}
async function signup(c, local) {
  const result = await c.auth.signUp({ email: `${local}-${run}@example.test`, password: 'RegressionPassword2026!' });
  check(!result.error, `Supabase SDK signup: ${result.error?.message}`);
  return result.data;
}

for (let attempt = 0; attempt < 60; attempt++) {
  try { if ((await fetch(`${base}/health`)).ok) break; } catch { /* startup */ }
  await new Promise(resolve => setTimeout(resolve, 500));
}

try {
  const login = await request('/platform/auth/login', { method: 'POST', auth: null, body: { email, password } });
  check(login.status === 200, 'platform login');
  token = login.data.token;
  const p = await createProject('security');
  const other = await createProject('isolated');
  const identity = await sql(p, "SELECT current_user, session_user, rolsuper, rolcreaterole, rolcreatedb, rolreplication, rolbypassrls FROM pg_roles WHERE rolname = current_user");
  check(identity.status === 200 && identity.data.rows[0][0].startsWith('dp_') && identity.data.rows[0][0] === identity.data.rows[0][1] && identity.data.rows[0].slice(2).every(value => value === false), 'actual tenant login has no cluster privilege');
  for (const query of ["CREATE ROLE forbidden_cluster_role", "SELECT rolpassword FROM pg_authid", "SET ROLE postgres", "SELECT pg_read_file('/etc/passwd')", "COPY (SELECT 1) TO PROGRAM '/bin/true'"]) {
    const result = await sql(p, query);
    check(result.status === 400, 'tenant SQL cannot gain operator or server-file/program authority');
  }
  const peer = await sql(p, `SELECT has_database_privilege(current_user, '${other.db_name}', 'CONNECT')`);
  check(peer.status === 200 && peer.data.rows[0][0] === false, 'project login cannot connect to another project');
  await sql(p, "SET application_name='tenant-modified-session'");
  const state = await sql(p, 'SHOW application_name');
  check(state.status === 200 && state.data.rows[0][0] !== 'tenant-modified-session', 'arbitrary SQL session state is discarded');
  const paged = await sql(p, 'SELECT generate_series(1,1005) AS value', true);
  check(paged.status === 200 && paged.data.row_count === 1000, 'read-only SQL closes limited results before commit');

  const c1 = client(p);
  const u1 = await signup(c1, 'user-one');
  check(!!u1.session?.access_token, 'autoconfirmed signup preserves SDK session shape');
  const c2 = client(p);
  const u2 = await signup(c2, 'user-two');
  check((await sql(p, 'CREATE TABLE public.protected_rows (id text PRIMARY KEY, user_id uuid NOT NULL, content text)')).status === 200, 'tenant DDL remains available');
  await sql(p, 'ALTER TABLE public.protected_rows ENABLE ROW LEVEL SECURITY');
  await sql(p, 'CREATE POLICY own_rows ON public.protected_rows TO authenticated USING (user_id=auth.uid()) WITH CHECK (user_id=auth.uid())');
  const service = client(p, p.service_role_key);
  const inserted = await service.from('protected_rows').insert([{ id: 'victim', user_id: u1.user.id, content: 'one' }, { id: 'victim&junk=1+#%文字', user_id: u2.user.id, content: 'two' }]).select();
  check(!inserted.error && inserted.data.length === 2, 'service-role CRUD preserves Supabase SDK behavior');
  const visible = await c1.from('protected_rows').select('*');
  check(!visible.error && visible.data.length === 1 && visible.data[0].id === 'victim', 'RLS confines user data');
  const anonymousRead = await client(p).from('protected_rows').select('*');
  check(!anonymousRead.error && anonymousRead.data.length === 0, 'RLS confines anonymous reads');
  const invalidBearer = await request('/rest/v1/protected_rows', { key: p.anon_key, bearer: 'invalid', auth: null });
  check(invalidBearer.status === 401, 'invalid bearer cannot silently fall back to anon');
  const invalidFilter = await request('/rest/v1/protected_rows?id=unsupported.value', { method: 'DELETE', auth: null, key: p.service_role_key });
  check(invalidFilter.status === 400, 'invalid filter cannot turn a mutation into a full-table operation');

  await sql(p, 'CREATE TABLE public.identities (category text, id text PRIMARY KEY, content text)');
  await sql(p, "INSERT INTO public.identities VALUES ('shared','a','one'),('shared','b','two')");
  const columns = await request(`/platform/projects/${p.id}/tables/identities/columns`);
  check(columns.status === 200 && columns.data.find(column => column.name === 'id').primary_key && columns.data.find(column => column.name === 'id').unique, 'actual row identity is exposed without replacing column metadata');
  for (const method of ['PATCH', 'DELETE']) {
    const unsafe = await request(`/platform/projects/${p.id}/tables/identities/rows?pk_column=category&pk_value=shared`, { method, ...(method === 'PATCH' ? { body: { content: 'must-not-write' } } : {}) });
    check(unsafe.status === 400, 'single-row mutation rejects a nonunique predicate');
  }
  const rows = await sql(p, 'SELECT count(*) FROM public.identities');
  check(rows.data.rows[0][0] === 2, 'rejected deletion leaves all rows intact');
  for (const schema of ['auth', 'pg_catalog', 'platform']) {
    const protectedTable = await request(`/platform/projects/${p.id}/tables/users/rows?schema=${schema}`);
    check(protectedTable.status === 400, 'table browser rejects protected schemas');
  }

  initialMode = (await request('/platform/admin/settings')).data.registration_mode;
  await request('/platform/admin/settings', {method:'PUT', body:{registration_mode:'open'}});
  const viewer = await request('/platform/auth/register', {method:'POST', auth:null, body:{email:`viewer-${run}@example.test`, password:'RegressionPassword2026!'}});
  check(viewer.status===201, 'disposable viewer registration');
  platformUsers.push(viewer.data.user.id);
  const orgList = await request('/platform/orgs');
  const orgId = orgList.data.find(org=>org.slug === `personal-${login.data.user.id}`).id;
  const invitation = await request(`/platform/orgs/${orgId}/invites`, {method:'POST', body:{email:viewer.data.user.email, role:'viewer'}});
  check(invitation.status===201, 'viewer invite creation');
  const accepted = await request(`/platform/orgs/invites/${invitation.data.token}/accept`, {method:'POST', auth:viewer.data.token});
  check(accepted.status===200, 'viewer invite acceptance');
  const viewerList = await request(`/platform/projects?org_id=${orgId}`, {auth:viewer.data.token});
  check(viewerList.status===200 && viewerList.data.every(project=>!project.service_role_key && !project.jwt_secret), 'viewer listing redacts privileged fields');
  const viewerDetail = await request(`/platform/projects/${p.id}`, {auth:viewer.data.token});
  check(viewerDetail.status===200 && !viewerDetail.data.service_role_key && !viewerDetail.data.jwt_secret && viewerDetail.data.anon_key, 'viewer detail retains shape without signing/write authority');
  const forbiddenSql = await request(`/platform/projects/${p.id}/sql`, {method:'POST', auth:viewer.data.token, body:{query:'SELECT 1'}});
  check(forbiddenSql.status===403, 'viewer cannot execute SQL');
  const owned=await request('/platform/projects',{method:'POST',auth:viewer.data.token,body:{name:`owned-delete-${run}`}});
  check(owned.status===201,'create account deletion fixture');
  const ownedQuery=()=>request(`/platform/projects/${owned.data.id}/sql`,{method:'POST',auth:viewer.data.token,body:{query:'SELECT 1'}});
  check((await ownedQuery()).status===200,'account deletion fixture has an active project pool');
  const viewerOrgs=await request('/platform/orgs',{auth:viewer.data.token});
  const ownedOrg=viewerOrgs.data.find(org=>org.slug===`personal-${viewer.data.user.id}`).id;
  const sharedInvite=await request(`/platform/orgs/${ownedOrg}/invites`,{method:'POST',auth:viewer.data.token,body:{email,role:'viewer'}});
  check(sharedInvite.status===201,'create shared owner-deletion fixture');
  check((await request(`/platform/orgs/invites/${sharedInvite.data.token}/accept`,{method:'POST'})).status===200,'join owner-deletion fixture');
  const guardedDelete=await request(`/platform/admin/users/${viewer.data.user.id}`,{method:'DELETE'});
  check(guardedDelete.status===400 && (await ownedQuery()).status===200,'owner deletion protects other members before project cleanup');
  check((await request(`/platform/orgs/${ownedOrg}/members/${login.data.user.id}`,{method:'DELETE',auth:viewer.data.token})).status===200,'remove only the fixture member');
  const privateDelete=await request(`/platform/admin/users/${viewer.data.user.id}`,{method:'DELETE'});
  check(privateDelete.status===200,`private account/project deletion: ${privateDelete.data.error}`);
  const removedDB=await sql(p,`SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname='${owned.data.db_name}')`);
  check(removedDB.status===200 && removedDB.data.rows[0][0]===false,'account deletion removes its live project database');
  await request('/platform/admin/settings', {method:'PUT',body:{registration_mode:'invite'}});
  const targeted = await request('/platform/admin/invites', {method:'POST',body:{email:`target-${run}@example.test`}});
  const wrongRecipient = await request('/platform/auth/register', {method:'POST',auth:null,body:{email:`wrong-${run}@example.test`,password:'RegressionPassword2026!',invite_code:targeted.data.code}});
  check(wrongRecipient.status===400, 'targeted invitation rejects another email');
  const oneUse = await request('/platform/admin/invites', {method:'POST',body:{}});
  const registrations = await Promise.all([1,2].map(n=>request('/platform/auth/register',{method:'POST',auth:null,body:{email:`race-${n}-${run}@example.test`,password:'RegressionPassword2026!',invite_code:oneUse.data.code}})));
  for (const registration of registrations) if(registration.status===201) platformUsers.push(registration.data.user.id);
  check(registrations.filter(result=>result.status===201).length===1, 'platform invite admits only one concurrent registration');
  await request('/platform/admin/settings',{method:'PUT',body:{registration_mode:initialMode}});

  const confirmedOff = await createProject('confirmation', { autoconfirm: false });
  const pending = await signup(client(confirmedOff), 'pending');
  check(pending.user && !pending.session, 'unconfirmed SDK signup returns a user without a session');
  const unconfirmedLogin = await request('/auth/v1/token?grant_type=password', { method: 'POST', auth: null, key: confirmedOff.anon_key, body: { email: pending.user.email, password: 'RegressionPassword2026!' } });
  check(unconfirmedLogin.status === 400, 'unconfirmed password login rejected');
  await sql(confirmedOff, `UPDATE auth.users SET email_confirmed_at=NOW() WHERE id='${pending.user.id}'`);
  const verified = await request('/auth/v1/token?grant_type=password',{method:'POST',auth:null,key:confirmedOff.anon_key,body:{email:pending.user.email,password:'RegressionPassword2026!'}});
  check(verified.status===200, 'trusted confirmation allows the existing password grant');
  const changedEmail = await request('/auth/v1/user',{method:'PUT',auth:null,key:confirmedOff.anon_key,bearer:verified.data.access_token,body:{email:`replacement-${run}@example.test`}});
  check(changedEmail.status===200 && !changedEmail.data.email_confirmed_at,'changing email clears confirmation');
  const unverifiedAccess = await request('/rest/v1/identities',{auth:null,key:confirmedOff.anon_key,bearer:verified.data.access_token});
  check(unverifiedAccess.status===401,'old JWT does not bypass replacement email confirmation');
  const anon = await request('/auth/v1/signup', { method: 'POST', auth: null, key: confirmedOff.anon_key, body: {} });
  check(anon.status === 200 && anon.data.user.is_anonymous, 'intentional anonymous signup retained');
  const refreshed = await request('/auth/v1/token?grant_type=refresh_token', { method: 'POST', auth: null, key: confirmedOff.anon_key, body: { refresh_token: anon.data.refresh_token } });
  const claims = JSON.parse(Buffer.from(refreshed.data.access_token.split('.')[1], 'base64url').toString());
  check(refreshed.status === 200 && refreshed.data.user.is_anonymous && claims.is_anonymous && claims.amr[0].method === 'anonymous', 'anonymous refresh preserves signed identity');
  const rotations = await Promise.all([1, 2].map(() => request('/auth/v1/token?grant_type=refresh_token', { method: 'POST', auth: null, key: confirmedOff.anon_key, body: { refresh_token: refreshed.data.refresh_token } })));
  check(rotations.filter(result => result.status === 200).length === 1, 'refresh token is consumed once under concurrency');
  const malformed = await fetch(`${base}/auth/v1/signup`, { method: 'POST', headers: { apikey: p.anon_key, 'Content-Type': 'application/json' }, body: '{malformed' });
  check(malformed.status === 400, 'malformed signup cannot create an anonymous account');

  await request(`/platform/projects/${p.id}/auth/users/${u1.user.id}/ban`, { method: 'POST' });
  const bannedPassword = await request('/auth/v1/token?grant_type=password', { method: 'POST', auth: null, key: p.anon_key, body: { email: u1.user.email, password: 'RegressionPassword2026!' } });
  check(bannedPassword.status === 403, 'infinity ban prevents password sessions');
  const bannedRefresh = await request('/auth/v1/token?grant_type=refresh_token', { method: 'POST', auth: null, key: p.anon_key, body: { refresh_token: u1.session.refresh_token } });
  check(bannedRefresh.status === 400, 'ban revokes refresh tokens');
  const bannedRead = await request('/rest/v1/protected_rows', { auth: null, key: p.anon_key, bearer: u1.session.access_token });
  check(bannedRead.status === 401, 'existing banned-user JWT cannot keep reading');
  const bannedDetail = await request(`/platform/projects/${p.id}/auth/users/${u1.user.id}`);
  check(bannedDetail.status === 200 && bannedDetail.data.banned_until, 'indefinite bans retain a valid management response');

  const disabled = await createProject('disabled', { enable_signup: false });
  const denied = await request('/auth/v1/signup', { method: 'POST', auth: null, key: disabled.anon_key, body: {} });
  check(denied.status === 403, 'disabled signup is enforced');
  const privateStorage = await request('/platform/backups/test-connection', { method: 'POST', body: { s3_endpoint: 'http://127.0.0.1:1', s3_region: 'us-east-1', s3_bucket: 'fixture', s3_access_key: 'fixture', s3_secret_key: 'fixture' } });
  check(privateStorage.status >= 400 && privateStorage.data.error.includes('BACKUP_ALLOWED_ENDPOINTS'), 'private S3 destination rejected before dialing');

  async function upload(project, text, filename, skipAuth = true) {
    const form = new FormData();
    form.set('file',new Blob([text],{type:'text/plain'}),filename);
    form.set('disable_triggers','false');
    form.set('skip_auth_schema',String(skipAuth));
    const response=await fetch(`${base}/platform/projects/${project.id}/import`,{method:'POST',headers:{Authorization:`Bearer ${token}`},body:form});
    const task=await response.json();
    check(response.status===202, 'import task contract');
    for(let attempt=0;attempt<100;attempt++) {
      const status=await request(`/platform/projects/${project.id}/import/${task.id}`);
      if(status.data.status!=='running') return status.data;
      await new Promise(resolve=>setTimeout(resolve,100));
    }
    assert.fail('import did not finish');
  }
  const importTarget=await createProject('imports');
  const imported=await upload(importTarget,`CREATE TABLE public.imported (id text PRIMARY KEY);
COPY public.imported (id) FROM stdin;
CREATE ROLE this_is_copy_data
\\.
ALTER TABLE public.imported ENABLE ROW LEVEL SECURITY;
CREATE POLICY deny_public ON public.imported USING (false);
`,'legitimate.sql');
  check(imported.status==='completed',`legitimate dump/COPY import: ${imported.error_message}`);
  const importedRows=await sql(importTarget,'SELECT id FROM public.imported');
  check(importedRows.data.rows[0][0]==='CREATE ROLE this_is_copy_data','public COPY data survives filtering');
  const rls=await sql(importTarget,"SELECT relrowsecurity FROM pg_class WHERE oid='public.imported'::regclass");
  check(rls.data.rows[0][0]===true,'import preserves RLS catalog state');
  const noRows=await client(importTarget).from('imported').select('*');
  check(!noRows.error && noRows.data.length===0,'import preserves anonymous RLS visibility');
  const shell=await upload(importTarget,"\\! printf untrusted-shell-command\n",'unsafe.sql');
  check(shell.status==='failed' && /restricted|restrict/i.test(shell.error_message),'psql rejects uploaded shell commands');
  const forbiddenServerSql=await upload(importTarget,'CREATE ROLE forbidden_import_role;\n','unsafe-role.sql',false);
  check(forbiddenServerSql.status==='failed','import worker cannot create cluster roles');

  check((await sql(importTarget, "CREATE TABLE public.auth (id text PRIMARY KEY)")).status===200,'create export fixture table');
  check((await sql(importTarget, "INSERT INTO public.auth VALUES ('public-data')")).status===200,'populate export fixture table');
  const exported = await fetch(`${base}/platform/projects/${importTarget.id}/export?format=custom`,{headers:{Authorization:`Bearer ${token}`}});
  check(exported.ok,'custom export remains available with restricted credentials');
  const customDump = new Uint8Array(await exported.arrayBuffer());
  check(new TextDecoder().decode(customDump.slice(0,5))==='PGDMP','custom export contains a PostgreSQL archive');
  const customTarget = await createProject('custom-imports');
  const custom = await upload(customTarget,customDump,'legitimate.dump');
  check(custom.status==='completed',`custom import remains compatible: ${custom.error_message}`);
  const publicAuth = await sql(customTarget,'SELECT id FROM public.auth');
  check(publicAuth.status===200 && publicAuth.data.rows[0][0]==='public-data',`custom filtering preserves a public table named auth: ${JSON.stringify(publicAuth)}`);

  // Cancellation must survive the worker's eventual error and restore triggers.
  check((await sql(customTarget, "CREATE TABLE public.trigger_probe (id int)")).status===200,'create trigger table');
  check((await sql(customTarget, "CREATE FUNCTION public.trigger_check() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$")).status===200,'create trigger function');
  check((await sql(customTarget, "CREATE TRIGGER fixture_trigger BEFORE INSERT ON public.trigger_probe FOR EACH ROW EXECUTE FUNCTION public.trigger_check()")).status===200,'create trigger');
  const slowForm = new FormData();
  slowForm.set('file',new Blob(['SELECT pg_sleep(5);\n']),'cancel.sql');
  slowForm.set('disable_triggers','true');
  const started = await fetch(`${base}/platform/projects/${customTarget.id}/import`,{method:'POST',headers:{Authorization:`Bearer ${token}`},body:slowForm});
  const slow = await started.json();
  check(started.status===202,'cancellable import starts');
  let disabledTriggers=false;
  for(let attempt=0;attempt<50;attempt++) {
    const state=await sql(customTarget,"SELECT tgenabled::text FROM pg_trigger WHERE tgname='fixture_trigger'");
    if(state.data.rows[0][0]==='D') {disabledTriggers=true;break;}
    await new Promise(resolve=>setTimeout(resolve,50));
  }
  check(disabledTriggers,'import disables user triggers before executing the dump');
  const cancelled=await request(`/platform/projects/${customTarget.id}/import/${slow.id}/cancel`,{method:'POST'});
  check(cancelled.status===200,'cancellation accepted');
  let restored=false;
  for(let attempt=0;attempt<50;attempt++) {
    const state=await sql(customTarget,"SELECT tgenabled::text FROM pg_trigger WHERE tgname='fixture_trigger'");
    if(state.data.rows[0][0]==='O') {restored=true;break;}
    await new Promise(resolve=>setTimeout(resolve,50));
  }
  const terminal=await request(`/platform/projects/${customTarget.id}/import/${slow.id}`);
  check(restored && terminal.data.status==='cancelled','cancelled worker preserves status and re-enables user triggers');

  if(process.env.DUPABASE_TEST_REST_MAX_BYTES) {
    const limit=Number(process.env.DUPABASE_TEST_REST_MAX_BYTES);
    assert.ok(limit>0 && limit<65536,'bounded-response fixture limit must be small');
    check((await sql(p,`CREATE TABLE public.large_result (payload text)`)).status===200,'create response-size fixture');
    check((await sql(p,`INSERT INTO public.large_result SELECT repeat('x',${limit+1000})`)).status===200,'populate response-size fixture');
    const oversized=await request('/rest/v1/large_result',{auth:null,key:p.service_role_key});
    check(oversized.status===400 && /smaller page/.test(oversized.data.message),`oversized REST results fail without silently truncating data: ${JSON.stringify(oversized)}`);
  }

  if(storageEndpoint) {
    const settings=await request('/platform/backups/settings',{method:'POST',body:{s3_endpoint:storageEndpoint,s3_region:'us-east-1',s3_bucket:'dupabase-maintenance',s3_access_key:'fixture-access',s3_secret_key:'fixture-secret',project_ids:[importTarget.id],platform_password:password}});
    check(settings.status===200,`fixture backup settings: ${settings.data.error}`);
    const connected=await request('/platform/backups/test-connection',{method:'POST',body:{s3_endpoint:storageEndpoint,s3_region:'us-east-1',s3_bucket:'dupabase-maintenance',s3_access_key:'fixture-access',s3_secret_key:'fixture-secret'}});
    check(connected.status===200,'operator-approved internal S3 storage remains supported');
    const backup=await request('/platform/backups/run',{method:'POST'});
    check(backup.status===200,'manual backup API');
    const history=(await request('/platform/backups/history')).data;
    const saved=history.find(entry=>entry.project_id===importTarget.id);
    check(saved?.status==='completed' && saved.size_bytes>0,`pg_dump upload completed: ${saved?.error_message}`);
    await sql(importTarget,"DELETE FROM public.auth");
    await sql(importTarget,"INSERT INTO public.auth VALUES ('changed-after-backup')");
    const restoration=await request(`/platform/backups/${saved.id}/restore`,{method:'POST',body:{platform_password:password}});
    check(restoration.status===202,'restore task contract preserved');
    let result;
    for(let attempt=0;attempt<100;attempt++) {
      result=(await request(`/platform/projects/${importTarget.id}/import/${restoration.data.task_id}`)).data;
      if(result.status!=='running') break;
      await new Promise(resolve=>setTimeout(resolve,100));
    }
    check(result?.status==='completed',`restore completes: ${result?.error_message}`);
    const recovered=await sql(importTarget,'SELECT id FROM public.auth');
    check(recovered.status===200 && recovered.data.rows[0][0]==='public-data','restore recovers actual saved data');
    const restoredRLS=await client(importTarget).from('imported').select('*');
    check(!restoredRLS.error && restoredRLS.data.length===0,'backup restore preserves RLS');
    await request('/platform/backups/settings',{method:'PATCH',body:{enabled:false,platform_password:password}});
  }

  console.log(`PASS: ${checks} isolated API and Supabase SDK security/compatibility checks`);
} finally {
  if(initialMode) await request('/platform/admin/settings',{method:'PUT',body:{registration_mode:initialMode}});
  for (const c of clients) c.auth.stopAutoRefresh();
  for (const project of projects.reverse()) {
    const result = await request(`/platform/projects/${project.id}`, { method: 'DELETE' });
    assert.equal(result.status, 200, 'disposable project cleanup');
  }
  for(const id of platformUsers) {
    const deleted=await request(`/platform/admin/users/${id}`,{method:'DELETE'});
    assert.ok([200,404].includes(deleted.status),'disposable platform user cleanup');
  }
}
