import { localTestURL } from './local_fixture.mjs';
import { createClient } from '@supabase/supabase-js';

const SUPABASE_URL = localTestURL();
const ANON_KEY = process.env.DUPABASE_TEST_ANON_KEY;
const SERVICE_ROLE_KEY = process.env.DUPABASE_TEST_SERVICE_ROLE_KEY;

if (!ANON_KEY || !SERVICE_ROLE_KEY) throw new Error('Set DUPABASE_TEST_ANON_KEY and DUPABASE_TEST_SERVICE_ROLE_KEY for the disposable project');

const supabase = createClient(SUPABASE_URL, ANON_KEY);
const adminClient = createClient(SUPABASE_URL, SERVICE_ROLE_KEY);

let passed = 0;
let failed = 0;

function assert(condition, testName) {
  if (condition) {
    console.log(`  PASS: ${testName}`);
    passed++;
  } else {
    console.log(`  FAIL: ${testName}`);
    failed++;
  }
}

async function run() {
  console.log('\n=== Testing @supabase/supabase-js compatibility ===\n');

  // Test 1: Sign up
  console.log('1. Auth: signUp');
  const email = `testuser_${Date.now()}@test.com`;
  const { data: signupData, error: signupError } = await supabase.auth.signUp({
    email,
    password: 'testpassword123',
    options: { data: { name: 'Test User' } },
  });
  assert(!signupError, 'signUp succeeds');
  assert(signupData.user?.email === email, 'signUp returns correct email');
  assert(signupData.session?.access_token, 'signUp returns access_token');
  assert(signupData.session?.refresh_token, 'signUp returns refresh_token');

  // Test 2: Sign in
  console.log('2. Auth: signInWithPassword');
  const { data: signinData, error: signinError } = await supabase.auth.signInWithPassword({
    email,
    password: 'testpassword123',
  });
  assert(!signinError, 'signIn succeeds');
  assert(signinData.session?.access_token, 'signIn returns access_token');
  assert(signinData.user?.email === email, 'signIn returns correct email');

  // Test 3: Get user
  console.log('3. Auth: getUser');
  const { data: userData, error: userError } = await supabase.auth.getUser();
  assert(!userError, 'getUser succeeds');
  assert(userData.user?.email === email, 'getUser returns correct email');

  // Test 4: Refresh session
  console.log('4. Auth: refreshSession');
  const { data: refreshData, error: refreshError } = await supabase.auth.refreshSession();
  assert(!refreshError, 'refreshSession succeeds');
  assert(refreshData.session?.access_token, 'refreshSession returns new access_token');

  // Test 5: REST - Select (using service_role for no RLS issues)
  console.log('5. REST: select');
  const { data: selectData, error: selectError } = await adminClient
    .from('todos')
    .select('*');
  assert(!selectError, 'select succeeds');
  assert(Array.isArray(selectData), 'select returns array');

  // Test 6: REST - Insert
  console.log('6. REST: insert');
  const { data: insertData, error: insertError } = await adminClient
    .from('todos')
    .insert({ title: 'From supabase-js', done: false })
    .select();
  assert(!insertError, 'insert succeeds');
  assert(insertData?.[0]?.title === 'From supabase-js', 'insert returns correct data');

  // Test 7: REST - Select with filters
  console.log('7. REST: select with filters');
  const { data: filterData, error: filterError } = await adminClient
    .from('todos')
    .select('id, title')
    .eq('done', false)
    .order('id', { ascending: false })
    .limit(1);
  assert(!filterError, 'filtered select succeeds');
  assert(filterData?.length <= 1, 'limit works');

  // Test 8: REST - Update
  console.log('8. REST: update');
  const todoId = insertData?.[0]?.id;
  const { data: updateData, error: updateError } = await adminClient
    .from('todos')
    .update({ done: true })
    .eq('id', todoId)
    .select();
  assert(!updateError, 'update succeeds');
  assert(updateData?.[0]?.done === true, 'update returns correct data');

  // Test 9: REST - Delete
  console.log('9. REST: delete');
  const { data: deleteData, error: deleteError } = await adminClient
    .from('todos')
    .delete()
    .eq('id', todoId)
    .select();
  assert(!deleteError, 'delete succeeds');
  assert(deleteData?.[0]?.id === todoId, 'delete returns deleted row');

  // Test 10: Auth - sign out
  console.log('10. Auth: signOut');
  const { error: signoutError } = await supabase.auth.signOut();
  assert(!signoutError, 'signOut succeeds');

  console.log(`\n=== Results: ${passed} passed, ${failed} failed ===\n`);
  process.exit(failed > 0 ? 1 : 0);
}

run().catch(err => {
  console.error('Test runner error:', err);
  process.exit(1);
});
