import { localTestURL } from './local_fixture.mjs';
/**
 * Admin Feature Test Suite
 *
 * Tests the admin panel functionality end-to-end:
 * 1. Registration mode (public endpoint)
 * 2. Admin user verification (is_admin flag)
 * 3. Admin: list users (paginated)
 * 4. Admin: get/update platform settings (registration mode)
 * 5. Admin: invite system (create, list, delete)
 * 6. Registration with invite code (invite-only mode)
 * 7. Registration when disabled
 * 8. Admin: delete user
 * 9. Non-admin cannot access admin endpoints
 * 10. Edge cases and validation
 */

const API = localTestURL();

const ADMIN_EMAIL = process.env.DUPABASE_TEST_ADMIN_EMAIL;
const ADMIN_PASSWORD = process.env.DUPABASE_TEST_ADMIN_PASSWORD;
if (!ADMIN_EMAIL || !ADMIN_PASSWORD) throw new Error('Set the disposable fixture administrator credentials');

// Test user credentials
const TEST_EMAIL = `admin_test_${Date.now()}@test.com`;
const TEST_PASSWORD = 'TestPassword123!';

let passed = 0;
let failed = 0;
const failures = [];
const createdInvites = new Set();
const createdUsers = new Set();
let adminToken;

function assert(condition, msg) {
  if (condition) {
    passed++;
    console.log(`  \x1b[32m\u2713\x1b[0m ${msg}`);
  } else {
    failed++;
    console.log(`  \x1b[31m\u2717\x1b[0m ${msg}`);
    failures.push(msg);
  }
}

async function api(method, path, body, token) {
  const h = { 'Content-Type': 'application/json' };
  if (token) h['Authorization'] = `Bearer ${token}`;
  const opts = { method, headers: h };
  if (body && method !== 'GET') opts.body = JSON.stringify(body);
  const res = await fetch(`${API}${path}`, opts);
  const data = await res.json().catch(() => null);
  if (res.ok && method === 'POST' && path === '/platform/admin/invites' && data?.id) createdInvites.add(data.id);
  if (res.ok && method === 'POST' && path === '/platform/auth/register' && data?.user?.id) createdUsers.add(data.user.id);
  return { status: res.status, data };
}

// Store original registration mode to restore at end
let originalMode;

async function run() {
  console.log('\n\x1b[1m=== ADMIN FEATURE TEST SUITE ===\x1b[0m\n');

  // ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  // SECTION 1: Admin Login & Verification
  // ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  console.log('\x1b[1m\u2500\u2500 Section 1: Admin Login & Verification \u2500\u2500\x1b[0m');

  // 1.1 Admin login
  const { status: adminLoginStatus, data: adminLoginData } = await api('POST', '/platform/auth/login', {
    email: ADMIN_EMAIL,
    password: ADMIN_PASSWORD,
  });
  assert(adminLoginStatus === 200, 'Admin login returns 200');
  assert(adminLoginData?.token, 'Admin login returns token');
  assert(adminLoginData?.user?.is_admin === true, 'Admin user has is_admin=true');
  assert(adminLoginData?.user?.email === ADMIN_EMAIL, 'Admin email matches');
  adminToken = adminLoginData?.token;
  if (!adminToken || !adminLoginData?.user?.is_admin) throw new Error('Disposable administrator login failed');

  // 1.2 Admin /me endpoint includes is_admin
  const { status: meStatus, data: meData } = await api('GET', '/platform/auth/me', null, adminToken);
  assert(meStatus === 200, 'GET /me returns 200');
  assert(meData?.is_admin === true, '/me returns is_admin=true for admin');

  // ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  // SECTION 2: Registration Mode (Public)
  // ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  console.log('\n\x1b[1m\u2500\u2500 Section 2: Registration Mode (Public Endpoint) \u2500\u2500\x1b[0m');

  // 2.1 Public registration-mode endpoint
  const { status: regModeStatus, data: regModeData } = await api('GET', '/platform/auth/registration-mode');
  assert(regModeStatus === 200, 'GET /registration-mode returns 200');
  assert(['open', 'invite', 'disabled'].includes(regModeData?.registration_mode), 'registration_mode is valid');
  originalMode = regModeData?.registration_mode;
  if (!['open', 'invite', 'disabled'].includes(originalMode)) throw new Error('Cannot save original registration mode');

  // ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  // SECTION 3: Admin Settings
  // ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  console.log('\n\x1b[1m\u2500\u2500 Section 3: Admin Settings \u2500\u2500\x1b[0m');

  // 3.1 Get settings
  const { status: getSettingsStatus, data: settingsData } = await api('GET', '/platform/admin/settings', null, adminToken);
  assert(getSettingsStatus === 200, 'GET /admin/settings returns 200');
  assert(settingsData?.registration_mode, 'Settings include registration_mode');

  // 3.2 Update to "open" (ensure clean state)
  const { status: setOpenStatus } = await api('PUT', '/platform/admin/settings', {
    registration_mode: 'open',
  }, adminToken);
  assert(setOpenStatus === 200, 'Set registration_mode to "open" returns 200');

  // 3.3 Verify change
  const { data: verifyOpen } = await api('GET', '/platform/auth/registration-mode');
  assert(verifyOpen?.registration_mode === 'open', 'Public endpoint reflects "open" mode');

  // 3.4 Update to "invite"
  const { status: setInviteStatus } = await api('PUT', '/platform/admin/settings', {
    registration_mode: 'invite',
  }, adminToken);
  assert(setInviteStatus === 200, 'Set registration_mode to "invite" returns 200');

  const { data: verifyInvite } = await api('GET', '/platform/auth/registration-mode');
  assert(verifyInvite?.registration_mode === 'invite', 'Public endpoint reflects "invite" mode');

  // 3.5 Update to "disabled"
  const { status: setDisabledStatus } = await api('PUT', '/platform/admin/settings', {
    registration_mode: 'disabled',
  }, adminToken);
  assert(setDisabledStatus === 200, 'Set registration_mode to "disabled" returns 200');

  const { data: verifyDisabled } = await api('GET', '/platform/auth/registration-mode');
  assert(verifyDisabled?.registration_mode === 'disabled', 'Public endpoint reflects "disabled" mode');

  // 3.6 Invalid mode
  const { status: invalidModeStatus } = await api('PUT', '/platform/admin/settings', {
    registration_mode: 'bogus',
  }, adminToken);
  assert(invalidModeStatus === 400, 'Invalid registration_mode returns 400');

  // ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  // SECTION 4: Registration When Disabled
  // ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  console.log('\n\x1b[1m\u2500\u2500 Section 4: Registration When Disabled \u2500\u2500\x1b[0m');

  // 4.1 Cannot register when disabled
  const { status: regDisabledStatus, data: regDisabledData } = await api('POST', '/platform/auth/register', {
    email: `blocked_${Date.now()}@test.com`,
    password: TEST_PASSWORD,
  });
  assert(regDisabledStatus === 403, 'Register when disabled returns 403');
  assert(regDisabledData?.error?.includes('disabled'), 'Error message mentions disabled');

  // ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  // SECTION 5: Invite System
  // ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  console.log('\n\x1b[1m\u2500\u2500 Section 5: Invite System \u2500\u2500\x1b[0m');

  // Switch to invite mode
  await api('PUT', '/platform/admin/settings', { registration_mode: 'invite' }, adminToken);

  // 5.1 Create invite (no email)
  const { status: createInvStatus, data: inv1 } = await api('POST', '/platform/admin/invites', {}, adminToken);
  assert(createInvStatus === 201, 'Create invite returns 201');
  assert(inv1?.code?.length === 32, 'Invite code is 32 hex chars');
  assert(inv1?.id, 'Invite has id');
  assert(inv1?.expires_at, 'Invite has expires_at');
  assert(inv1?.used_by === null, 'Invite not used yet');

  // 5.2 Create invite with email
  const { status: createInv2Status, data: inv2 } = await api('POST', '/platform/admin/invites', {
    email: 'specific@test.com',
  }, adminToken);
  assert(createInv2Status === 201, 'Create invite with email returns 201');
  assert(inv2?.email === 'specific@test.com', 'Invite email is set');

  // 5.3 Create invite with custom expiry
  const { status: createInv3Status, data: inv3 } = await api('POST', '/platform/admin/invites', {
    expires_in_hours: 1,
  }, adminToken);
  assert(createInv3Status === 201, 'Create invite with custom expiry returns 201');
  // Expires should be ~1 hour from now, not 72
  const expiresIn = new Date(inv3?.expires_at).getTime() - Date.now();
  assert(expiresIn < 2 * 60 * 60 * 1000, 'Custom expiry invite expires within 2 hours');

  // 5.4 List invites
  const { status: listInvStatus, data: invites } = await api('GET', '/platform/admin/invites', null, adminToken);
  assert(listInvStatus === 200, 'List invites returns 200');
  assert(Array.isArray(invites), 'Invites is an array');
  assert(invites.length >= 3, 'At least 3 invites exist');

  // 5.5 Cannot register without invite code in invite mode
  const { status: noCodeStatus } = await api('POST', '/platform/auth/register', {
    email: `noinvite_${Date.now()}@test.com`,
    password: TEST_PASSWORD,
  });
  assert(noCodeStatus === 400, 'Register without invite code in invite mode returns 400');

  // 5.6 Cannot register with invalid invite code
  const { status: badCodeStatus } = await api('POST', '/platform/auth/register', {
    email: `badinvite_${Date.now()}@test.com`,
    password: TEST_PASSWORD,
    invite_code: 'deadbeefdeadbeefdeadbeefdeadbeef',
  });
  assert(badCodeStatus === 400, 'Register with invalid invite code returns 400');

  // 5.7 Register with valid invite code
  const { status: invRegStatus, data: invRegData } = await api('POST', '/platform/auth/register', {
    email: TEST_EMAIL,
    password: TEST_PASSWORD,
    invite_code: inv1.code,
  });
  assert(invRegStatus === 201, 'Register with valid invite code returns 201');
  assert(invRegData?.token, 'Invite registration returns token');
  assert(invRegData?.user?.is_admin === false, 'Invited user is not admin');
  const testToken = invRegData?.token;
  const testUserId = invRegData?.user?.id;

  // 5.8 Invite is now used
  const { data: invitesAfterUse } = await api('GET', '/platform/admin/invites', null, adminToken);
  const usedInvite = invitesAfterUse?.find(i => i.id === inv1.id);
  assert(usedInvite?.used_by === testUserId, 'Invite used_by is set to registered user');
  assert(usedInvite?.used_at !== null, 'Invite used_at is set');

  // 5.9 Cannot reuse same invite code
  const { status: reuseStatus } = await api('POST', '/platform/auth/register', {
    email: `reuse_${Date.now()}@test.com`,
    password: TEST_PASSWORD,
    invite_code: inv1.code,
  });
  assert(reuseStatus === 400, 'Cannot reuse already-used invite code');

  // 5.10 Delete invite
  const { status: delInvStatus } = await api('DELETE', `/platform/admin/invites/${inv2.id}`, null, adminToken);
  assert(delInvStatus === 200, 'Delete invite returns 200');

  // 5.11 Deleted invite no longer in list
  const { data: invitesAfterDel } = await api('GET', '/platform/admin/invites', null, adminToken);
  const deletedInv = invitesAfterDel?.find(i => i.id === inv2.id);
  assert(!deletedInv, 'Deleted invite not in list');

  // 5.12 Delete nonexistent invite
  const { status: del404Status } = await api('DELETE', '/platform/admin/invites/00000000-0000-0000-0000-000000000000', null, adminToken);
  assert(del404Status === 404, 'Delete nonexistent invite returns 404');

  // ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  // SECTION 6: List Users (Paginated)
  // ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  console.log('\n\x1b[1m\u2500\u2500 Section 6: List Users (Paginated) \u2500\u2500\x1b[0m');

  // 6.1 Default pagination
  const { status: listUsersStatus, data: usersPage1 } = await api('GET', '/platform/admin/users', null, adminToken);
  assert(listUsersStatus === 200, 'List users returns 200');
  assert(usersPage1?.users?.length > 0, 'Users array has entries');
  assert(typeof usersPage1?.total === 'number', 'Response includes total count');
  assert(usersPage1?.page === 1, 'Default page is 1');
  assert(usersPage1?.per_page === 20, 'Default per_page is 20');

  // 6.2 Custom page size
  const { data: usersSmall } = await api('GET', '/platform/admin/users?page=1&per_page=3', null, adminToken);
  assert(usersSmall?.users?.length <= 3, 'per_page=3 returns at most 3 users');
  assert(usersSmall?.per_page === 3, 'per_page reflects requested value');
  assert(usersSmall?.total === usersPage1?.total, 'Total count is consistent across page sizes');

  // 6.3 Page 2
  const { data: usersP2 } = await api('GET', '/platform/admin/users?page=2&per_page=3', null, adminToken);
  assert(usersP2?.page === 2, 'Page 2 returned');
  assert(usersP2?.users?.length > 0, 'Page 2 has users');
  // Ensure no overlap with page 1
  const p1Ids = new Set(usersSmall?.users?.map(u => u.id));
  const p2Overlap = usersP2?.users?.some(u => p1Ids.has(u.id));
  assert(!p2Overlap, 'Page 2 users do not overlap with page 1');

  // 6.4 User object shape
  const sampleUser = usersPage1?.users?.[0];
  assert(sampleUser?.id, 'User has id');
  assert(sampleUser?.email, 'User has email');
  assert(sampleUser?.pg_username, 'User has pg_username');
  assert(typeof sampleUser?.is_admin === 'boolean', 'User has is_admin boolean');
  assert(typeof sampleUser?.project_count === 'number', 'User has project_count');
  assert(sampleUser?.created_at, 'User has created_at');

  // 6.5 Out of range page returns empty
  const { data: usersOOB } = await api('GET', '/platform/admin/users?page=9999&per_page=20', null, adminToken);
  assert(usersOOB?.users?.length === 0, 'Out-of-range page returns empty array');
  assert(usersOOB?.total === usersPage1?.total, 'Total count still correct on empty page');

  // ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  // SECTION 7: Non-Admin Access Control
  // ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  console.log('\n\x1b[1m\u2500\u2500 Section 7: Non-Admin Access Control \u2500\u2500\x1b[0m');

  // 7.1 Non-admin cannot list users
  const { status: noAdminUsersStatus } = await api('GET', '/platform/admin/users', null, testToken);
  assert(noAdminUsersStatus === 403, 'Non-admin GET /admin/users returns 403');

  // 7.2 Non-admin cannot get settings
  const { status: noAdminSettingsStatus } = await api('GET', '/platform/admin/settings', null, testToken);
  assert(noAdminSettingsStatus === 403, 'Non-admin GET /admin/settings returns 403');

  // 7.3 Non-admin cannot update settings
  const { status: noAdminUpdateStatus } = await api('PUT', '/platform/admin/settings', {
    registration_mode: 'open',
  }, testToken);
  assert(noAdminUpdateStatus === 403, 'Non-admin PUT /admin/settings returns 403');

  // 7.4 Non-admin cannot list invites
  const { status: noAdminInvitesStatus } = await api('GET', '/platform/admin/invites', null, testToken);
  assert(noAdminInvitesStatus === 403, 'Non-admin GET /admin/invites returns 403');

  // 7.5 Non-admin cannot create invites
  const { status: noAdminCreateInvStatus } = await api('POST', '/platform/admin/invites', {}, testToken);
  assert(noAdminCreateInvStatus === 403, 'Non-admin POST /admin/invites returns 403');

  // 7.6 Non-admin cannot delete users
  const { status: noAdminDelUserStatus } = await api('DELETE', `/platform/admin/users/${testUserId}`, null, testToken);
  assert(noAdminDelUserStatus === 403, 'Non-admin DELETE /admin/users returns 403');

  // 7.7 No auth at all
  const { status: noAuthUsersStatus } = await api('GET', '/platform/admin/users');
  assert(noAuthUsersStatus === 401, 'Unauthenticated GET /admin/users returns 401');

  // ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  // SECTION 8: Delete User
  // ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  console.log('\n\x1b[1m\u2500\u2500 Section 8: Delete User \u2500\u2500\x1b[0m');

  // 8.1 Admin cannot delete self
  const adminId = adminLoginData?.user?.id;
  const { status: selfDelStatus, data: selfDelData } = await api('DELETE', `/platform/admin/users/${adminId}`, null, adminToken);
  assert(selfDelStatus === 400, 'Admin cannot delete self (400)');
  assert(selfDelData?.error?.includes('yourself'), 'Error mentions self-deletion');

  // 8.2 Cannot delete another admin (if any exist — create scenario by checking)
  // Note: the EnsureAdmin user is the only admin, so this is effectively covered

  // 8.3 Delete the test user
  const { status: delUserStatus } = await api('DELETE', `/platform/admin/users/${testUserId}`, null, adminToken);
  assert(delUserStatus === 200, 'Delete test user returns 200');

  // 8.4 Deleted user cannot login
  const { status: delUserLoginStatus } = await api('POST', '/platform/auth/login', {
    email: TEST_EMAIL,
    password: TEST_PASSWORD,
  });
  assert(delUserLoginStatus === 401, 'Deleted user cannot login');

  const consumedAgain = await api('POST', '/platform/auth/register', {
    email: `reused_${Date.now()}@test.com`, password: TEST_PASSWORD, invite_code: inv1.code,
  });
  assert(consumedAgain.status === 400, 'Deleting an invited user never reopens the invitation');

  // 8.5 Delete nonexistent user
  const { status: del404UserStatus } = await api('DELETE', '/platform/admin/users/00000000-0000-0000-0000-000000000000', null, adminToken);
  assert(del404UserStatus === 404, 'Delete nonexistent user returns 404');

  // 8.6 Total user count decreased (test user was included in section 6 count)
  const { data: usersAfterDel } = await api('GET', '/platform/admin/users', null, adminToken);
  assert(usersAfterDel?.total === usersPage1?.total - 1, 'Total count decreased by 1 after delete');

  // ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  // SECTION 9: Open Registration
  // ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  console.log('\n\x1b[1m\u2500\u2500 Section 9: Open Registration \u2500\u2500\x1b[0m');

  // Restore open mode
  await api('PUT', '/platform/admin/settings', { registration_mode: 'open' }, adminToken);

  // 9.1 Can register without invite in open mode
  const openEmail = `open_${Date.now()}@test.com`;
  const { status: openRegStatus, data: openRegData } = await api('POST', '/platform/auth/register', {
    email: openEmail,
    password: TEST_PASSWORD,
  });
  assert(openRegStatus === 201, 'Register in open mode returns 201');
  assert(openRegData?.user?.is_admin === false, 'Newly registered user is not admin');

  // 9.2 New user shows in admin list
  const { data: usersWithNew } = await api('GET', '/platform/admin/users?per_page=100', null, adminToken);
  const newUser = usersWithNew?.users?.find(u => u.email === openEmail);
  assert(!!newUser, 'Newly registered user appears in admin users list');

  // Cleanup: delete the open-reg test user
  if (newUser) {
    await api('DELETE', `/platform/admin/users/${newUser.id}`, null, adminToken);
  }

  // ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  // RESULTS
  // ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  console.log(`\n\x1b[1m=== RESULTS ===\x1b[0m`);
  console.log(`  \x1b[32mPassed: ${passed}\x1b[0m`);
  if (failed > 0) {
    console.log(`  \x1b[31mFailed: ${failed}\x1b[0m`);
    console.log('\n  Failed tests:');
    failures.forEach(f => console.log(`    - ${f}`));
  }
  console.log(`  Total: ${passed + failed}\n`);

  process.exitCode = failed > 0 ? 1 : 0;
}

async function cleanup() {
  if (!adminToken) return;
  if (originalMode) {
    const restored = await api('PUT', '/platform/admin/settings', { registration_mode: originalMode }, adminToken);
    if (restored.status !== 200) throw new Error('Registration mode restoration failed');
  }
  for (const id of createdInvites) {
    const deleted = await api('DELETE', `/platform/admin/invites/${encodeURIComponent(id)}`, null, adminToken);
    if (![200, 204, 404].includes(deleted.status)) throw new Error('Test invitation cleanup failed');
  }
  for (const id of createdUsers) {
    const deleted = await api('DELETE', `/platform/admin/users/${encodeURIComponent(id)}`, null, adminToken);
    if (![200, 204, 404].includes(deleted.status)) throw new Error('Test user cleanup failed');
  }
}

run().catch(err => {
  console.error('\x1b[31mFATAL:\x1b[0m', err.message);
  process.exitCode = 1;
}).finally(cleanup).catch(err => {
  console.error('Cleanup failed:', err.message);
  process.exitCode = 1;
});
