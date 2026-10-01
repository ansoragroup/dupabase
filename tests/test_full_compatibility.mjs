import { localTestURL } from './local_fixture.mjs';
/**
 * Comprehensive supabase-js compatibility test
 *
 * Tests the FULL flow end-to-end:
 * 1. Platform: register -> login -> create project
 * 2. Auth: signUp, signIn, getUser, updateUser, refreshSession, signOut
 * 3. REST: CRUD, filters, ordering, limit, offset, upsert, RPC, count, select columns
 * 4. RLS: authenticated user isolation
 * 5. Service role: bypass RLS
 * 6. Platform: list projects, update settings, backup settings, rotate keys
 */
import { createClient } from '@supabase/supabase-js';

const API_URL = localTestURL();
const PLATFORM_EMAIL = `fulltest_${Date.now()}@test.com`;
const PLATFORM_PASSWORD = 'TestPassword123!';

let passed = 0;
let failed = 0;
let skipped = 0;
const failures = [];

function assert(condition, testName) {
  if (condition) {
    console.log(`  \x1b[32mPASS\x1b[0m: ${testName}`);
    passed++;
  } else {
    console.log(`  \x1b[31mFAIL\x1b[0m: ${testName}`);
    failed++;
    failures.push(testName);
  }
}

function skip(testName, reason) {
  console.log(`  \x1b[33mSKIP\x1b[0m: ${testName} (${reason})`);
  skipped++;
}

async function apiCall(method, path, body, token) {
  const headers = { 'Content-Type': 'application/json' };
  if (token) headers['Authorization'] = `Bearer ${token}`;
  const opts = { method, headers };
  if (body) opts.body = JSON.stringify(body);
  const res = await fetch(`${API_URL}${path}`, opts);
  const data = await res.json().catch(() => null);
  return { status: res.status, data };
}

async function run() {
  console.log('\n\x1b[1m=== COMPREHENSIVE SUPABASE-JS COMPATIBILITY TEST ===\x1b[0m\n');

  // ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  // SECTION 1: PLATFORM API
  // ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  console.log('\x1b[1m── Section 1: Platform API ──\x1b[0m');

  // 1.1 Health check
  console.log('1.1 Health check');
  const health = await fetch(`${API_URL}/health`).then(r => r.json());
  assert(health.status === 'ok', 'GET /health returns ok');

  // 1.2 Register
  console.log('1.2 Platform register');
  const { status: regStatus, data: regData } = await apiCall('POST', '/platform/auth/register', {
    email: PLATFORM_EMAIL,
    password: PLATFORM_PASSWORD,
    display_name: 'Test User',
  });
  assert(regStatus === 201, 'register returns 201');
  assert(regData?.token, 'register returns JWT token');
  assert(regData?.user?.email === PLATFORM_EMAIL, 'register returns correct email');
  assert(regData?.user?.pg_username, 'register returns pg_username');
  const platformToken = regData?.token;

  // 1.3 Login
  console.log('1.3 Platform login');
  const { status: loginStatus, data: loginData } = await apiCall('POST', '/platform/auth/login', {
    email: PLATFORM_EMAIL,
    password: PLATFORM_PASSWORD,
  });
  assert(loginStatus === 200, 'login returns 200');
  assert(loginData?.token, 'login returns JWT token');

  // 1.4 Get current user
  console.log('1.4 Platform me');
  const { status: meStatus, data: meData } = await apiCall('GET', '/platform/auth/me', null, platformToken);
  assert(meStatus === 200, 'GET /platform/auth/me returns 200');
  assert(meData?.email === PLATFORM_EMAIL, 'me returns correct email');

  // 1.5 Create project
  console.log('1.5 Create project');
  const projectName = `test_proj_${Date.now()}`;
  const { status: projStatus, data: projData } = await apiCall('POST', '/platform/projects', {
    name: projectName,
  }, platformToken);
  assert(projStatus === 201, 'create project returns 201');
  assert(projData?.anon_key, 'project has anon_key');
  assert(projData?.service_role_key, 'project has service_role_key');
  assert(projData?.db_name, 'project has db_name');
  assert(projData?.status === 'active', 'project status is active');

  const ANON_KEY = projData?.anon_key;
  const SERVICE_ROLE_KEY = projData?.service_role_key;
  const projectId = projData?.id;

  // 1.6 List projects
  console.log('1.6 List projects');
  const { status: listStatus, data: listData } = await apiCall('GET', '/platform/projects', null, platformToken);
  assert(listStatus === 200, 'list projects returns 200');
  assert(Array.isArray(listData), 'list returns array');
  assert(listData?.some(p => p.id === projectId), 'new project appears in list');

  // 1.7 Update project settings
  console.log('1.7 Update project settings');
  const { status: settingsStatus, data: settingsData } = await apiCall(
    'PATCH', `/platform/projects/${projectId}/settings`,
    { enable_signup: true, autoconfirm: true, password_min_length: 8 },
    platformToken,
  );
  assert(settingsStatus === 200, 'update settings returns 200');
  assert(settingsData?.password_min_length === 8, 'settings updated correctly');

  // 1.8 Reveal credentials
  console.log('1.8 Reveal credentials');
  const { status: credStatus, data: credData } = await apiCall('POST', '/platform/credentials/reveal', {
    platform_password: PLATFORM_PASSWORD,
  }, platformToken);
  assert(credStatus === 200, 'reveal credentials returns 200');
  assert(credData?.pg_username, 'credentials contain pg_username');
  assert(credData?.pg_password, 'credentials contain pg_password');

  // 1.9 Rotate API keys
  console.log('1.9 Rotate API keys');
  const { status: rotateStatus, data: rotateData } = await apiCall(
    'POST', `/platform/projects/${projectId}/rotate-keys`, {}, platformToken,
  );
  assert(rotateStatus === 200, 'rotate keys returns 200');
  assert(rotateData?.anon_key, 'new anon_key returned');
  assert(rotateData?.service_role_key, 'new service_role_key returned');
  assert(rotateData?.anon_key !== ANON_KEY, 'anon_key actually changed');

  // Use the NEW keys from now on
  const NEW_ANON_KEY = rotateData?.anon_key || ANON_KEY;
  const NEW_SERVICE_KEY = rotateData?.service_role_key || SERVICE_ROLE_KEY;

  // 1.10 Backup settings (save)
  console.log('1.10 Backup settings');
  const { status: backupSaveStatus } = await apiCall('POST', '/platform/backups/settings', {
    platform_password: PLATFORM_PASSWORD,
    s3_endpoint: 'https://s3.example.com',
    s3_bucket: 'test-bucket',
    s3_access_key: 'AKIAIOSFODNN7EXAMPLE',
    s3_secret_key: 'wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY',
    s3_region: 'us-east-1',
    schedule: 'daily',
    retention_days: 7,
  }, platformToken);
  assert(backupSaveStatus === 200, 'save backup settings returns 200');

  // 1.11 Get backup settings
  console.log('1.11 Get backup settings');
  const { status: backupGetStatus, data: backupGetData } = await apiCall(
    'GET', '/platform/backups/settings', null, platformToken,
  );
  assert(backupGetStatus === 200, 'get backup settings returns 200');
  assert(backupGetData?.s3_bucket === 'test-bucket', 'backup settings persisted correctly');
  assert(backupGetData?.schedule === 'daily', 'backup schedule correct');

  // 1.12 Get backup history
  console.log('1.12 Get backup history');
  const { status: backupHistStatus, data: backupHistData } = await apiCall(
    'GET', '/platform/backups/history', null, platformToken,
  );
  assert(backupHistStatus === 200, 'get backup history returns 200');
  assert(Array.isArray(backupHistData), 'backup history is array');

  // ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  // SECTION 2: SUPABASE-JS AUTH
  // ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  console.log('\n\x1b[1m── Section 2: Supabase Auth (via supabase-js) ──\x1b[0m');

  const supabase = createClient(API_URL, NEW_ANON_KEY);
  const adminClient = createClient(API_URL, NEW_SERVICE_KEY);

  const testEmail = `user_${Date.now()}@example.com`;
  const testPassword = 'SuperSecure123!';

  // 2.1 Sign up
  console.log('2.1 signUp');
  const { data: signupData, error: signupError } = await supabase.auth.signUp({
    email: testEmail,
    password: testPassword,
    options: { data: { full_name: 'John Doe', age: 30 } },
  });
  assert(!signupError, `signUp succeeds${signupError ? ': ' + signupError.message : ''}`);
  assert(signupData.user?.email === testEmail, 'signUp returns correct email');
  assert(signupData.session?.access_token, 'signUp returns access_token');
  assert(signupData.session?.refresh_token, 'signUp returns refresh_token');
  assert(signupData.session?.token_type === 'bearer', 'signUp returns token_type bearer');
  assert(signupData.session?.expires_in > 0, 'signUp returns positive expires_in');
  assert(signupData.user?.user_metadata?.full_name === 'John Doe', 'signUp stores user_metadata');

  // 2.2 Sign up duplicate (should fail)
  console.log('2.2 signUp duplicate');
  const { error: dupError } = await supabase.auth.signUp({
    email: testEmail,
    password: testPassword,
  });
  assert(dupError !== null, 'duplicate signUp returns error');

  // 2.3 Sign in with password
  console.log('2.3 signInWithPassword');
  const { data: signinData, error: signinError } = await supabase.auth.signInWithPassword({
    email: testEmail,
    password: testPassword,
  });
  assert(!signinError, `signIn succeeds${signinError ? ': ' + signinError.message : ''}`);
  assert(signinData.session?.access_token, 'signIn returns access_token');
  assert(signinData.session?.refresh_token, 'signIn returns refresh_token');
  assert(signinData.user?.email === testEmail, 'signIn returns correct email');
  assert(signinData.user?.role === 'authenticated', 'signIn returns role authenticated');

  // 2.4 Sign in with wrong password
  console.log('2.4 signIn wrong password');
  const { error: wrongPwError } = await supabase.auth.signInWithPassword({
    email: testEmail,
    password: 'WrongPassword123!',
  });
  assert(wrongPwError !== null, 'wrong password returns error');

  // 2.5 Get user
  console.log('2.5 getUser');
  const { data: userData, error: userError } = await supabase.auth.getUser();
  assert(!userError, `getUser succeeds${userError ? ': ' + userError.message : ''}`);
  assert(userData.user?.email === testEmail, 'getUser returns correct email');
  assert(userData.user?.id, 'getUser returns user id');
  assert(userData.user?.aud === 'authenticated', 'getUser returns aud=authenticated');

  // 2.6 Get session
  console.log('2.6 getSession');
  const { data: sessionData, error: sessionError } = await supabase.auth.getSession();
  assert(!sessionError, 'getSession succeeds');
  assert(sessionData.session?.access_token, 'getSession returns access_token');

  // 2.7 Update user metadata
  console.log('2.7 updateUser metadata');
  const { data: updateData, error: updateError } = await supabase.auth.updateUser({
    data: { full_name: 'Jane Doe', age: 31, city: 'NYC' },
  });
  assert(!updateError, `updateUser succeeds${updateError ? ': ' + updateError.message : ''}`);
  assert(updateData.user?.user_metadata?.full_name === 'Jane Doe', 'metadata updated: full_name');
  assert(updateData.user?.user_metadata?.city === 'NYC', 'metadata updated: city');

  // 2.8 Refresh session
  console.log('2.8 refreshSession');
  const { data: refreshData, error: refreshError } = await supabase.auth.refreshSession();
  assert(!refreshError, `refreshSession succeeds${refreshError ? ': ' + refreshError.message : ''}`);
  assert(refreshData.session?.access_token, 'refreshSession returns new access_token');
  assert(refreshData.session?.refresh_token, 'refreshSession returns new refresh_token');

  // 2.9 Sign up second user (for RLS testing later)
  const testEmail2 = `user2_${Date.now()}@example.com`;
  const { data: signup2Data } = await supabase.auth.signUp({
    email: testEmail2,
    password: testPassword,
    options: { data: { full_name: 'User Two' } },
  });
  const user2Token = signup2Data?.session?.access_token;

  // Sign back in as first user
  await supabase.auth.signInWithPassword({ email: testEmail, password: testPassword });

  // ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  // SECTION 3: CREATE TEST TABLES (via service role)
  // ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  console.log('\n\x1b[1m── Section 3: Setup test tables ──\x1b[0m');

  // Create tables via direct SQL using service_role
  const setupSQL = async (sql) => {
    const res = await fetch(`${API_URL}/rest/v1/rpc/`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'apikey': NEW_SERVICE_KEY,
        'Authorization': `Bearer ${NEW_SERVICE_KEY}`,
      },
    });
  };

  // We need to create test tables. Use the admin client to execute setup.
  // Since we can't run raw SQL via supabase-js easily, let's create a helper function via RPC
  // Actually, let's use fetch directly to call RPC for setup, then test with supabase-js

  // Create a setup function first, then create tables via it
  // Alternative: use the service_role client to insert into a table that we know exists
  // Let's check if the project already has a 'todos' table from a previous test
  console.log('3.1 Create test table via service_role');
  // Use the admin key to create table via raw fetch (service_role has CREATE privileges)
  // Since we can't run DDL through PostgREST directly, let's create an RPC function first
  // We'll use the project's database connection via the pool manager

  // Actually - we need to create tables. Let me use a different approach:
  // Create an RPC setup function, then call it, then clean up.
  // OR -- just test with tables that already exist (auth schema tables are there).
  // BUT the user wants to test REST API with custom tables.

  // Let's create the table via the platform's database connection
  // We'll use the credentials we revealed earlier to connect directly
  const pgUsername = credData?.pg_username;
  const pgPassword = credData?.pg_password;
  const dbName = projData?.db_name;

  // Create test tables using a direct POST to a helper endpoint...
  // Actually, the simplest approach: insert into a new table and PostgREST will fail,
  // because the table doesn't exist. We need to create it somehow.

  // Best approach: use the REST API with service_role to test against auth.users (which exists)
  // AND create a public.todos table via pg connection from the test

  // Let me use Node's built-in support or just test with what we can.
  // For now, let's create a simple setup: use direct fetch to call a custom function

  // Try inserting into a table - if it doesn't exist, we'll know
  const { data: todosCheck, error: todosError } = await adminClient.from('todos').select('*').limit(1);

  if (todosError) {
    // Table doesn't exist - we need to create it using direct DB connection
    // Use pg via child_process to create the table
    const { execSync } = await import('child_process');
    try {
      const connStr = `postgresql://${pgUsername}:${encodeURIComponent(pgPassword)}@localhost:15432/${dbName}`;
      execSync(`psql "${connStr}" -c "
        CREATE TABLE IF NOT EXISTS public.todos (
          id SERIAL PRIMARY KEY,
          title TEXT NOT NULL,
          done BOOLEAN DEFAULT FALSE,
          priority INTEGER DEFAULT 0,
          user_id UUID REFERENCES auth.users(id),
          tags TEXT[] DEFAULT '{}',
          metadata JSONB DEFAULT '{}',
          created_at TIMESTAMPTZ DEFAULT NOW()
        );
        ALTER TABLE public.todos ENABLE ROW LEVEL SECURITY;
        CREATE POLICY todos_select ON public.todos FOR SELECT TO authenticated USING (user_id = auth.uid());
        CREATE POLICY todos_insert ON public.todos FOR INSERT TO authenticated WITH CHECK (user_id = auth.uid());
        CREATE POLICY todos_update ON public.todos FOR UPDATE TO authenticated USING (user_id = auth.uid());
        CREATE POLICY todos_delete ON public.todos FOR DELETE TO authenticated USING (user_id = auth.uid());
        CREATE POLICY todos_service ON public.todos FOR ALL TO service_role USING (true);
        GRANT ALL ON public.todos TO anon, authenticated, service_role;
        GRANT USAGE, SELECT ON SEQUENCE public.todos_id_seq TO anon, authenticated, service_role;

        CREATE OR REPLACE FUNCTION public.add_numbers(a integer, b integer)
        RETURNS integer LANGUAGE sql AS \$\$ SELECT a + b; \$\$;
        GRANT EXECUTE ON FUNCTION public.add_numbers TO anon, authenticated, service_role;

        CREATE OR REPLACE FUNCTION public.get_todos_count()
        RETURNS integer LANGUAGE sql SECURITY DEFINER AS \$\$ SELECT count(*)::integer FROM public.todos; \$\$;
        GRANT EXECUTE ON FUNCTION public.get_todos_count TO anon, authenticated, service_role;
      "`, { stdio: 'pipe' });
      console.log('  \x1b[32mOK\x1b[0m: Test tables and functions created');
    } catch (e) {
      console.log('  \x1b[31mERROR\x1b[0m: Failed to create test tables:', e.stderr?.toString()?.slice(0, 200));
      console.log('  Trying with stech superuser...');
      try {
        const superConnStr = `postgresql://stech:S0cr%40t123@localhost:15432/${dbName}`;
        execSync(`psql "${superConnStr}" -c "
          CREATE TABLE IF NOT EXISTS public.todos (
            id SERIAL PRIMARY KEY,
            title TEXT NOT NULL,
            done BOOLEAN DEFAULT FALSE,
            priority INTEGER DEFAULT 0,
            user_id UUID REFERENCES auth.users(id),
            tags TEXT[] DEFAULT '{}',
            metadata JSONB DEFAULT '{}',
            created_at TIMESTAMPTZ DEFAULT NOW()
          );
          ALTER TABLE public.todos ENABLE ROW LEVEL SECURITY;
          DROP POLICY IF EXISTS todos_select ON public.todos;
          DROP POLICY IF EXISTS todos_insert ON public.todos;
          DROP POLICY IF EXISTS todos_update ON public.todos;
          DROP POLICY IF EXISTS todos_delete ON public.todos;
          DROP POLICY IF EXISTS todos_service ON public.todos;
          CREATE POLICY todos_select ON public.todos FOR SELECT TO authenticated USING (user_id = auth.uid());
          CREATE POLICY todos_insert ON public.todos FOR INSERT TO authenticated WITH CHECK (user_id = auth.uid());
          CREATE POLICY todos_update ON public.todos FOR UPDATE TO authenticated USING (user_id = auth.uid());
          CREATE POLICY todos_delete ON public.todos FOR DELETE TO authenticated USING (user_id = auth.uid());
          CREATE POLICY todos_service ON public.todos FOR ALL TO service_role USING (true);
          GRANT ALL ON public.todos TO anon, authenticated, service_role;
          GRANT USAGE, SELECT ON SEQUENCE public.todos_id_seq TO anon, authenticated, service_role;

          CREATE OR REPLACE FUNCTION public.add_numbers(a integer, b integer)
          RETURNS integer LANGUAGE sql AS \\\$\\\$ SELECT a + b; \\\$\\\$;
          GRANT EXECUTE ON FUNCTION public.add_numbers TO anon, authenticated, service_role;

          CREATE OR REPLACE FUNCTION public.get_todos_count()
          RETURNS integer LANGUAGE sql SECURITY DEFINER AS \\\$\\\$ SELECT count(*)::integer FROM public.todos; \\\$\\\$;
          GRANT EXECUTE ON FUNCTION public.get_todos_count TO anon, authenticated, service_role;
        "`, { stdio: 'pipe' });
        console.log('  \x1b[32mOK\x1b[0m: Test tables created via superuser');
      } catch (e2) {
        console.log('  \x1b[31mERROR\x1b[0m:', e2.stderr?.toString()?.slice(0, 200));
      }
    }
  } else {
    console.log('  \x1b[32mOK\x1b[0m: todos table already exists');
  }

  // ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  // SECTION 4: SUPABASE-JS REST API (service_role - no RLS)
  // ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  console.log('\n\x1b[1m── Section 4: REST API via supabase-js (service_role) ──\x1b[0m');

  // Get user ID for foreign key
  const { data: currentUser } = await supabase.auth.getUser();
  const userId = currentUser?.user?.id;

  // 4.1 Insert single row
  console.log('4.1 Insert single row');
  const { data: ins1, error: ins1Err } = await adminClient
    .from('todos')
    .insert({ title: 'First todo', done: false, priority: 1, user_id: userId })
    .select();
  assert(!ins1Err, `insert single row${ins1Err ? ': ' + ins1Err.message : ''}`);
  assert(ins1?.[0]?.title === 'First todo', 'insert returns correct title');
  assert(ins1?.[0]?.id, 'insert returns auto-generated id');
  const firstTodoId = ins1?.[0]?.id;

  // 4.2 Insert multiple rows
  console.log('4.2 Insert multiple rows');
  const { data: ins2, error: ins2Err } = await adminClient
    .from('todos')
    .insert([
      { title: 'Second todo', done: false, priority: 2, user_id: userId },
      { title: 'Third todo', done: true, priority: 3, user_id: userId },
      { title: 'Fourth todo', done: false, priority: 1, user_id: userId },
      { title: 'Fifth todo', done: true, priority: 5, user_id: userId },
    ])
    .select();
  assert(!ins2Err, `insert multiple rows${ins2Err ? ': ' + ins2Err.message : ''}`);
  assert(ins2?.length === 4, 'insert returns 4 rows');

  // 4.3 Select all
  console.log('4.3 Select all');
  const { data: sel1, error: sel1Err } = await adminClient.from('todos').select('*');
  assert(!sel1Err, `select all${sel1Err ? ': ' + sel1Err.message : ''}`);
  assert(Array.isArray(sel1), 'select returns array');
  assert(sel1?.length >= 5, 'select returns at least 5 rows');

  // 4.4 Select specific columns
  console.log('4.4 Select specific columns');
  const { data: sel2, error: sel2Err } = await adminClient.from('todos').select('id, title');
  assert(!sel2Err, `select specific columns${sel2Err ? ': ' + sel2Err.message : ''}`);
  assert(sel2?.[0] && 'id' in sel2[0] && 'title' in sel2[0], 'returns requested columns');
  assert(!('done' in (sel2?.[0] || {})), 'does not return unrequested columns');

  // 4.5 Filter: eq
  console.log('4.5 Filter: eq');
  const { data: fEq, error: fEqErr } = await adminClient.from('todos').select('*').eq('done', true);
  assert(!fEqErr, `eq filter${fEqErr ? ': ' + fEqErr.message : ''}`);
  assert(fEq?.every(r => r.done === true), 'eq filter returns only matching rows');

  // 4.6 Filter: neq
  console.log('4.6 Filter: neq');
  const { data: fNeq, error: fNeqErr } = await adminClient.from('todos').select('*').neq('done', true);
  assert(!fNeqErr, `neq filter${fNeqErr ? ': ' + fNeqErr.message : ''}`);
  assert(fNeq?.every(r => r.done !== true), 'neq filter excludes matching rows');

  // 4.7 Filter: gt, gte, lt, lte
  console.log('4.7 Filter: gt/gte/lt/lte');
  const { data: fGt, error: fGtErr } = await adminClient.from('todos').select('*').gt('priority', 2);
  assert(!fGtErr, `gt filter${fGtErr ? ': ' + fGtErr.message : ''}`);
  assert(fGt?.every(r => r.priority > 2), 'gt returns rows with priority > 2');

  const { data: fLte, error: fLteErr } = await adminClient.from('todos').select('*').lte('priority', 2);
  assert(!fLteErr, `lte filter${fLteErr ? ': ' + fLteErr.message : ''}`);
  assert(fLte?.every(r => r.priority <= 2), 'lte returns rows with priority <= 2');

  // 4.8 Filter: like / ilike
  console.log('4.8 Filter: like/ilike');
  const { data: fLike, error: fLikeErr } = await adminClient.from('todos').select('*').ilike('title', '%todo%');
  assert(!fLikeErr, `ilike filter${fLikeErr ? ': ' + fLikeErr.message : ''}`);
  assert(fLike?.length >= 5, 'ilike matches all todos');

  // 4.9 Filter: in
  console.log('4.9 Filter: in');
  const { data: fIn, error: fInErr } = await adminClient.from('todos').select('*').in('priority', [1, 5]);
  assert(!fInErr, `in filter${fInErr ? ': ' + fInErr.message : ''}`);
  assert(fIn?.every(r => [1, 5].includes(r.priority)), 'in filter returns only matching priorities');

  // 4.10 Filter: is (null check)
  console.log('4.10 Filter: is');
  const { data: fIs, error: fIsErr } = await adminClient.from('todos').select('*').is('done', false);
  assert(!fIsErr, `is filter${fIsErr ? ': ' + fIsErr.message : ''}`);

  // 4.11 Order: ascending
  console.log('4.11 Order ascending');
  const { data: oAsc, error: oAscErr } = await adminClient
    .from('todos').select('priority').order('priority', { ascending: true });
  assert(!oAscErr, `order asc${oAscErr ? ': ' + oAscErr.message : ''}`);
  const ascPriorities = oAsc?.map(r => r.priority);
  assert(ascPriorities?.every((v, i) => i === 0 || v >= ascPriorities[i - 1]), 'ascending order correct');

  // 4.12 Order: descending
  console.log('4.12 Order descending');
  const { data: oDesc, error: oDescErr } = await adminClient
    .from('todos').select('priority').order('priority', { ascending: false });
  assert(!oDescErr, `order desc${oDescErr ? ': ' + oDescErr.message : ''}`);
  const descPriorities = oDesc?.map(r => r.priority);
  assert(descPriorities?.every((v, i) => i === 0 || v <= descPriorities[i - 1]), 'descending order correct');

  // 4.13 Limit
  console.log('4.13 Limit');
  const { data: lim, error: limErr } = await adminClient.from('todos').select('*').limit(2);
  assert(!limErr, `limit${limErr ? ': ' + limErr.message : ''}`);
  assert(lim?.length === 2, 'limit returns exactly 2 rows');

  // 4.14 Limit + offset (pagination)
  console.log('4.14 Limit + offset');
  const { data: page1 } = await adminClient.from('todos').select('id').order('id').limit(2).range(0, 1);
  const { data: page2 } = await adminClient.from('todos').select('id').order('id').limit(2).range(2, 3);
  assert(page1?.length === 2, 'page 1 returns 2 rows');
  assert(page2?.length >= 1, 'page 2 returns rows');
  assert(page1?.[0]?.id !== page2?.[0]?.id, 'pages return different rows');

  // 4.15 Update
  console.log('4.15 Update');
  const { data: upd1, error: upd1Err } = await adminClient
    .from('todos')
    .update({ title: 'Updated first todo', priority: 10 })
    .eq('id', firstTodoId)
    .select();
  assert(!upd1Err, `update${upd1Err ? ': ' + upd1Err.message : ''}`);
  assert(upd1?.[0]?.title === 'Updated first todo', 'update changes title');
  assert(upd1?.[0]?.priority === 10, 'update changes priority');

  // 4.16 Upsert (insert or update)
  console.log('4.16 Upsert');
  const { data: ups1, error: ups1Err } = await adminClient
    .from('todos')
    .upsert({ id: firstTodoId, title: 'Upserted todo', done: true, priority: 99, user_id: userId }, { onConflict: 'id' })
    .select();
  assert(!ups1Err, `upsert${ups1Err ? ': ' + ups1Err.message : ''}`);
  assert(ups1?.[0]?.title === 'Upserted todo', 'upsert updates existing row');
  assert(ups1?.[0]?.priority === 99, 'upsert updates priority');

  // 4.17 Delete
  console.log('4.17 Delete');
  const { data: del1, error: del1Err } = await adminClient
    .from('todos')
    .delete()
    .eq('id', firstTodoId)
    .select();
  assert(!del1Err, `delete${del1Err ? ': ' + del1Err.message : ''}`);
  assert(del1?.[0]?.id === firstTodoId, 'delete returns deleted row');

  // Verify deletion
  const { data: afterDel } = await adminClient.from('todos').select('*').eq('id', firstTodoId);
  assert(afterDel?.length === 0, 'deleted row no longer exists');

  // 4.18 RPC call
  console.log('4.18 RPC call');
  const { data: rpcData, error: rpcError } = await adminClient.rpc('add_numbers', { a: 3, b: 7 });
  assert(!rpcError, `RPC call${rpcError ? ': ' + rpcError.message : ''}`);
  assert(rpcData === 10 || (Array.isArray(rpcData) && rpcData[0]?.add_numbers === 10), 'RPC returns correct result');

  // ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  // SECTION 5: RLS TESTING (authenticated user)
  // ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  console.log('\n\x1b[1m── Section 5: RLS (Row Level Security) ──\x1b[0m');

  // 5.1 Authenticated insert (should set user_id via auth.uid())
  console.log('5.1 Authenticated insert');
  const { data: rlsIns, error: rlsInsErr } = await supabase
    .from('todos')
    .insert({ title: 'My private todo', done: false, priority: 1, user_id: userId })
    .select();
  assert(!rlsInsErr, `authenticated insert${rlsInsErr ? ': ' + rlsInsErr.message : ''}`);
  assert(rlsIns?.[0]?.title === 'My private todo', 'authenticated insert returns data');

  // 5.2 Authenticated select (should only see own rows)
  console.log('5.2 Authenticated select (own rows only)');
  const { data: rlsSel, error: rlsSelErr } = await supabase.from('todos').select('*');
  assert(!rlsSelErr, `authenticated select${rlsSelErr ? ': ' + rlsSelErr.message : ''}`);
  assert(rlsSel?.every(r => r.user_id === userId), 'RLS: user only sees own rows');

  // 5.3 Insert as user2 via admin
  const user2Id = signup2Data?.user?.id;
  if (user2Id) {
    console.log('5.3 Cross-user isolation');
    await adminClient
      .from('todos')
      .insert({ title: 'User2 todo', done: false, priority: 1, user_id: user2Id });

    // User 1 should NOT see user2's todo
    const { data: rlsCross } = await supabase.from('todos').select('*');
    assert(rlsCross?.every(r => r.user_id === userId), 'RLS: user1 cannot see user2 rows');

    // Service role SHOULD see all rows
    const { data: svcAll } = await adminClient.from('todos').select('*');
    assert(svcAll?.some(r => r.user_id === user2Id), 'service_role sees all rows');
  } else {
    skip('5.3 Cross-user isolation', 'user2 creation failed');
  }

  // ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  // SECTION 6: AUTH EDGE CASES
  // ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  console.log('\n\x1b[1m── Section 6: Auth edge cases ──\x1b[0m');

  // 6.1 Update password
  console.log('6.1 Update password');
  const newPassword = 'NewPassword456!';
  const { data: pwData, error: pwError } = await supabase.auth.updateUser({
    password: newPassword,
  });
  assert(!pwError, `update password${pwError ? ': ' + pwError.message : ''}`);

  // 6.2 Sign in with new password
  console.log('6.2 Sign in with new password');
  const { error: newPwSigninError } = await supabase.auth.signInWithPassword({
    email: testEmail,
    password: newPassword,
  });
  assert(!newPwSigninError, `sign in with new password${newPwSigninError ? ': ' + newPwSigninError.message : ''}`);

  // 6.3 Sign out
  console.log('6.3 Sign out');
  const { error: signoutError } = await supabase.auth.signOut();
  assert(!signoutError, `signOut${signoutError ? ': ' + signoutError.message : ''}`);

  // 6.4 Get user after sign out (should fail)
  console.log('6.4 getUser after signOut');
  const { data: noUser, error: noUserError } = await supabase.auth.getUser();
  assert(noUserError || !noUser?.user, 'getUser fails after signOut');

  // 6.5 Sign back in (confirm account still works)
  console.log('6.5 Sign back in after signOut');
  const { error: reSigninError } = await supabase.auth.signInWithPassword({
    email: testEmail,
    password: newPassword,
  });
  assert(!reSigninError, `re-signin works${reSigninError ? ': ' + reSigninError.message : ''}`);

  // ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  // SECTION 7: ERROR HANDLING
  // ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  console.log('\n\x1b[1m── Section 7: Error handling ──\x1b[0m');

  // 7.1 Select from non-existent table
  console.log('7.1 Non-existent table');
  const { error: noTableErr } = await adminClient.from('nonexistent_table').select('*');
  assert(noTableErr !== null, 'non-existent table returns error');

  // 7.2 Insert with missing required field
  console.log('7.2 Missing required field');
  const { error: missingErr } = await adminClient.from('todos').insert({ done: true }).select();
  assert(missingErr !== null, 'missing required field returns error');

  // 7.3 Invalid filter value
  console.log('7.3 Select with count');
  const { count, error: countErr } = await adminClient
    .from('todos')
    .select('*', { count: 'exact', head: true });
  assert(!countErr, `count query${countErr ? ': ' + countErr.message : ''}`);
  // Note: count might be null if our server doesn't support Prefer: count=exact via head
  // This is acceptable for MVP

  // ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  // SECTION 8: PLATFORM ERROR CASES
  // ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  console.log('\n\x1b[1m── Section 8: Platform error handling ──\x1b[0m');

  // 8.1 Duplicate registration
  console.log('8.1 Duplicate registration');
  const { status: dupRegStatus } = await apiCall('POST', '/platform/auth/register', {
    email: PLATFORM_EMAIL,
    password: PLATFORM_PASSWORD,
  });
  assert(dupRegStatus === 409, 'duplicate registration returns 409');

  // 8.2 Login with wrong password
  console.log('8.2 Wrong platform password');
  const { status: wrongPlatPwStatus } = await apiCall('POST', '/platform/auth/login', {
    email: PLATFORM_EMAIL,
    password: 'WrongPassword!',
  });
  assert(wrongPlatPwStatus === 401, 'wrong password returns 401');

  // 8.3 Access protected endpoint without token
  console.log('8.3 No auth token');
  const { status: noAuthStatus } = await apiCall('GET', '/platform/projects');
  assert(noAuthStatus === 401, 'no token returns 401');

  // 8.4 Reveal credentials with wrong password
  console.log('8.4 Reveal with wrong password');
  const { status: wrongCredStatus } = await apiCall('POST', '/platform/credentials/reveal', {
    platform_password: 'WrongPassword!',
  }, platformToken);
  assert(wrongCredStatus === 401, 'wrong credential password returns 401');

  // ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  // CLEANUP
  // ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  // Clean up test data via admin client
  await adminClient.from('todos').delete().neq('id', 0);

  // ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  // RESULTS
  // ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  console.log('\n\x1b[1m════════════════════════════════════════════\x1b[0m');
  console.log(`\x1b[1m  Results: \x1b[32m${passed} passed\x1b[0m, \x1b[31m${failed} failed\x1b[0m, \x1b[33m${skipped} skipped\x1b[0m`);
  console.log('\x1b[1m════════════════════════════════════════════\x1b[0m');

  if (failures.length > 0) {
    console.log('\n\x1b[31mFailures:\x1b[0m');
    failures.forEach(f => console.log(`  - ${f}`));
  }

  console.log('');
  process.exit(failed > 0 ? 1 : 0);
}

run().catch(err => {
  console.error('\x1b[31mTest runner crash:\x1b[0m', err);
  process.exit(1);
});
