import { localTestURL } from './local_fixture.mjs';
/**
 * Supabase Drop-in Compatibility Test
 *
 * Proves that ANY app using @supabase/supabase-js can switch to our platform
 * by ONLY changing the URL and keys — zero code changes.
 *
 * Simulates a realistic blog/notes app with:
 *   - public.profiles   (anon-readable, owner-writable)
 *   - public.posts      (anon-readable, authenticated-writable)
 *   - public.comments   (anon-readable, authenticated-writable)
 *   - public.bookmarks  (private per-user)
 *   - RPC functions      (public helpers)
 *
 * Test flow:
 *   1. Platform setup: register, create project, seed schema
 *   2. Anonymous client: read public data, blocked from writing
 *   3. Auth: signUp, signIn, getUser, updateUser, refreshSession, signOut
 *   4. Authenticated CRUD: insert, select, update, delete with RLS
 *   5. Multi-user isolation: two users can't see each other's private data
 *   6. Advanced queries: filters, ordering, pagination, upsert, count, RPC
 *   7. Realtime-style patterns: subscriptions setup (no actual realtime, just client API)
 */
import { createClient } from '@supabase/supabase-js';
import { execSync } from 'child_process';
import { writeFileSync, unlinkSync } from 'fs';
import { tmpdir } from 'os';
import { join } from 'path';

const API_URL = localTestURL();
const PLATFORM_PASSWORD = 'Platform$ecure1!';

let passed = 0;
let failed = 0;
let skipped = 0;
const failures = [];

function assert(condition, name, detail) {
  if (condition) {
    console.log(`  \x1b[32mPASS\x1b[0m ${name}`);
    passed++;
  } else {
    console.log(`  \x1b[31mFAIL\x1b[0m ${name}${detail ? ' — ' + detail : ''}`);
    failed++;
    failures.push(name);
  }
}

function skip(name, reason) {
  console.log(`  \x1b[33mSKIP\x1b[0m ${name} (${reason})`);
  skipped++;
}

function section(title) {
  console.log(`\n\x1b[1m── ${title} ──\x1b[0m`);
}

async function platformAPI(method, path, body, token) {
  const headers = { 'Content-Type': 'application/json' };
  if (token) headers['Authorization'] = `Bearer ${token}`;
  const opts = { method, headers };
  if (body) opts.body = JSON.stringify(body);
  const res = await fetch(`${API_URL}${path}`, opts);
  return { status: res.status, data: await res.json().catch(() => null) };
}

/**
 * Run SQL against a project database using psql.
 * Writes SQL to a temp file to avoid shell escaping issues with $$.
 */
function runSQL(dbName, sql) {
  const tmpFile = join(tmpdir(), `supabase_test_${Date.now()}.sql`);
  writeFileSync(tmpFile, sql, 'utf8');
  try {
    execSync(
      `psql "postgresql://stech:S0cr%40t123@localhost:15432/${dbName}" -f "${tmpFile}"`,
      { stdio: 'pipe' },
    );
  } finally {
    try { unlinkSync(tmpFile); } catch {}
  }
}

// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
async function run() {
  console.log('\n\x1b[1m=== SUPABASE DROP-IN COMPATIBILITY TEST ===\x1b[0m');
  console.log('Proves: change URL + keys, everything works.\n');

  // ──────────────────────────────────────
  section('1. Platform Setup');
  // ──────────────────────────────────────

  const email = `dropin_${Date.now()}@test.com`;
  const { data: reg } = await platformAPI('POST', '/platform/auth/register', {
    email, password: PLATFORM_PASSWORD,
  });
  const token = reg.token;
  assert(!!token, '1.1 Platform register');

  const { data: proj } = await platformAPI('POST', '/platform/projects', {
    name: `dropin_${Date.now()}`,
  }, token);
  assert(proj?.status === 'active', '1.2 Create project');

  const ANON_KEY = proj.anon_key;
  const SERVICE_KEY = proj.service_role_key;
  const DB_NAME = proj.db_name;

  // ──────────────────────────────────────
  section('2. Seed Database Schema');
  // ──────────────────────────────────────

  // Create realistic app tables with RLS
  runSQL(DB_NAME, `
-- Profiles: public-readable, owner-writable
CREATE TABLE public.profiles (
  id UUID PRIMARY KEY REFERENCES auth.users(id) ON DELETE CASCADE,
  username TEXT UNIQUE NOT NULL,
  display_name TEXT,
  bio TEXT DEFAULT '',
  avatar_url TEXT,
  created_at TIMESTAMPTZ DEFAULT NOW()
);
ALTER TABLE public.profiles ENABLE ROW LEVEL SECURITY;
CREATE POLICY profiles_read  ON public.profiles FOR SELECT TO anon, authenticated USING (true);
CREATE POLICY profiles_write ON public.profiles FOR INSERT TO authenticated WITH CHECK (id = auth.uid());
CREATE POLICY profiles_own_update ON public.profiles FOR UPDATE TO authenticated USING (id = auth.uid());
CREATE POLICY profiles_own_delete ON public.profiles FOR DELETE TO authenticated USING (id = auth.uid());
CREATE POLICY profiles_service ON public.profiles FOR ALL TO service_role USING (true);
GRANT ALL ON public.profiles TO anon, authenticated, service_role;

-- Posts: public-readable, authenticated-writable (own posts)
CREATE TABLE public.posts (
  id SERIAL PRIMARY KEY,
  author_id UUID NOT NULL REFERENCES auth.users(id),
  title TEXT NOT NULL,
  body TEXT DEFAULT '',
  published BOOLEAN DEFAULT FALSE,
  category TEXT DEFAULT 'general',
  view_count INTEGER DEFAULT 0,
  tags TEXT[] DEFAULT '{}',
  metadata JSONB DEFAULT '{}',
  created_at TIMESTAMPTZ DEFAULT NOW(),
  updated_at TIMESTAMPTZ DEFAULT NOW()
);
ALTER TABLE public.posts ENABLE ROW LEVEL SECURITY;
-- Anyone can read published posts
CREATE POLICY posts_public_read ON public.posts FOR SELECT TO anon, authenticated
  USING (published = true OR author_id = auth.uid());
CREATE POLICY posts_insert ON public.posts FOR INSERT TO authenticated
  WITH CHECK (author_id = auth.uid());
CREATE POLICY posts_own_update ON public.posts FOR UPDATE TO authenticated
  USING (author_id = auth.uid());
CREATE POLICY posts_own_delete ON public.posts FOR DELETE TO authenticated
  USING (author_id = auth.uid());
CREATE POLICY posts_service ON public.posts FOR ALL TO service_role USING (true);
GRANT ALL ON public.posts TO anon, authenticated, service_role;
GRANT USAGE, SELECT ON SEQUENCE public.posts_id_seq TO anon, authenticated, service_role;

-- Comments: public-readable, authenticated-writable
CREATE TABLE public.comments (
  id SERIAL PRIMARY KEY,
  post_id INTEGER NOT NULL REFERENCES public.posts(id) ON DELETE CASCADE,
  user_id UUID NOT NULL REFERENCES auth.users(id),
  body TEXT NOT NULL,
  created_at TIMESTAMPTZ DEFAULT NOW()
);
ALTER TABLE public.comments ENABLE ROW LEVEL SECURITY;
CREATE POLICY comments_read ON public.comments FOR SELECT TO anon, authenticated USING (true);
CREATE POLICY comments_insert ON public.comments FOR INSERT TO authenticated
  WITH CHECK (user_id = auth.uid());
CREATE POLICY comments_own_update ON public.comments FOR UPDATE TO authenticated
  USING (user_id = auth.uid());
CREATE POLICY comments_own_delete ON public.comments FOR DELETE TO authenticated
  USING (user_id = auth.uid());
CREATE POLICY comments_service ON public.comments FOR ALL TO service_role USING (true);
GRANT ALL ON public.comments TO anon, authenticated, service_role;
GRANT USAGE, SELECT ON SEQUENCE public.comments_id_seq TO anon, authenticated, service_role;

-- Bookmarks: fully private per-user
CREATE TABLE public.bookmarks (
  id SERIAL PRIMARY KEY,
  user_id UUID NOT NULL REFERENCES auth.users(id),
  post_id INTEGER NOT NULL REFERENCES public.posts(id) ON DELETE CASCADE,
  note TEXT DEFAULT '',
  created_at TIMESTAMPTZ DEFAULT NOW(),
  UNIQUE(user_id, post_id)
);
ALTER TABLE public.bookmarks ENABLE ROW LEVEL SECURITY;
CREATE POLICY bookmarks_own ON public.bookmarks FOR ALL TO authenticated
  USING (user_id = auth.uid()) WITH CHECK (user_id = auth.uid());
CREATE POLICY bookmarks_service ON public.bookmarks FOR ALL TO service_role USING (true);
-- No anon policy: anonymous users cannot see bookmarks at all
GRANT ALL ON public.bookmarks TO authenticated, service_role;
GRANT USAGE, SELECT ON SEQUENCE public.bookmarks_id_seq TO authenticated, service_role;

-- RPC: public helper functions
CREATE OR REPLACE FUNCTION public.get_post_stats(target_post_id integer)
RETURNS TABLE(comment_count bigint, bookmark_count bigint) LANGUAGE sql STABLE SECURITY DEFINER AS
$fn$
  SELECT
    (SELECT count(*) FROM public.comments WHERE post_id = target_post_id),
    (SELECT count(*) FROM public.bookmarks WHERE post_id = target_post_id);
$fn$;
GRANT EXECUTE ON FUNCTION public.get_post_stats(integer) TO anon, authenticated, service_role;

CREATE OR REPLACE FUNCTION public.search_posts(keyword text)
RETURNS SETOF public.posts LANGUAGE sql STABLE SECURITY DEFINER AS
$fn$
  SELECT * FROM public.posts WHERE published = true AND (title ILIKE '%' || keyword || '%' OR body ILIKE '%' || keyword || '%');
$fn$;
GRANT EXECUTE ON FUNCTION public.search_posts(text) TO anon, authenticated, service_role;
  `);
  console.log('  \x1b[32mOK\x1b[0m Schema seeded (profiles, posts, comments, bookmarks, RPCs)');

  // ──────────────────────────────────────
  // Create supabase-js clients — THE ONLY THING A USER CHANGES
  // ──────────────────────────────────────
  //
  //  const supabase = createClient('https://xyz.supabase.co', 'eyJ...')   // <-- old
  //  const supabase = createClient('http://localhost:3333',    ANON_KEY)   // <-- new
  //
  const anonClient = createClient(API_URL, ANON_KEY);
  const adminClient = createClient(API_URL, SERVICE_KEY);

  // ──────────────────────────────────────
  section('3. Auth — Sign Up & Sign In');
  // ──────────────────────────────────────

  const user1Email = `alice_${Date.now()}@example.com`;
  const user1Pass  = 'Alice$ecure123!';
  const user2Email = `bob_${Date.now()}@example.com`;
  const user2Pass  = 'Bob$ecure456!';

  // 3.1 Sign up user 1 (Alice)
  const { data: su1, error: su1Err } = await anonClient.auth.signUp({
    email: user1Email,
    password: user1Pass,
    options: { data: { full_name: 'Alice Smith' } },
  });
  assert(!su1Err && su1.user?.id, '3.1 signUp Alice', su1Err?.message);
  assert(su1.session?.access_token, '3.2 Alice gets access_token');
  assert(su1.session?.refresh_token, '3.3 Alice gets refresh_token');
  assert(su1.user?.user_metadata?.full_name === 'Alice Smith', '3.4 Alice user_metadata persisted');

  const alice = anonClient; // Alice is now signed in on anonClient
  const aliceId = su1.user?.id;

  // 3.5 Sign up user 2 (Bob) on a separate client
  const bobClient = createClient(API_URL, ANON_KEY);
  const { data: su2, error: su2Err } = await bobClient.auth.signUp({
    email: user2Email,
    password: user2Pass,
    options: { data: { full_name: 'Bob Jones' } },
  });
  assert(!su2Err && su2.user?.id, '3.5 signUp Bob', su2Err?.message);
  const bobId = su2.user?.id;

  // 3.6 getUser
  const { data: { user: aliceUser } } = await alice.auth.getUser();
  assert(aliceUser?.email === user1Email, '3.6 getUser returns Alice email');
  assert(aliceUser?.role === 'authenticated', '3.7 getUser role = authenticated');

  // 3.8 updateUser
  const { data: upd } = await alice.auth.updateUser({ data: { city: 'Berlin' } });
  assert(upd.user?.user_metadata?.city === 'Berlin', '3.8 updateUser metadata');

  // 3.9 getSession
  const { data: sess } = await alice.auth.getSession();
  assert(!!sess.session?.access_token, '3.9 getSession returns token');

  // 3.10 refreshSession
  const { data: ref, error: refErr } = await alice.auth.refreshSession();
  assert(!refErr && ref.session?.access_token, '3.10 refreshSession', refErr?.message);

  // ──────────────────────────────────────
  section('4. Authenticated CRUD — Alice Seeds Data');
  // ──────────────────────────────────────

  // 4.1 Create profile
  const { data: prof, error: profErr } = await alice
    .from('profiles')
    .insert({ id: aliceId, username: 'alice', display_name: 'Alice Smith', bio: 'Hello world' })
    .select()
    .single();
  assert(!profErr && prof?.username === 'alice', '4.1 Insert profile', profErr?.message);

  // 4.2 Create published posts
  const { data: posts, error: postsErr } = await alice
    .from('posts')
    .insert([
      { author_id: aliceId, title: 'Getting Started with Supabase', body: 'Supabase is great...', published: true, category: 'tutorial', view_count: 150, tags: ['supabase', 'postgres'] },
      { author_id: aliceId, title: 'Advanced RLS Patterns', body: 'Row level security tips...', published: true, category: 'tutorial', view_count: 89, tags: ['rls', 'security'] },
      { author_id: aliceId, title: 'Draft: Upcoming Features', body: 'Coming soon...', published: false, category: 'news' },
      { author_id: aliceId, title: 'Why PostgreSQL Rocks', body: 'PostgreSQL is the best...', published: true, category: 'opinion', view_count: 320, tags: ['postgres'] },
      { author_id: aliceId, title: 'Building a Blog with Supabase', body: 'Tutorial on building blogs...', published: true, category: 'tutorial', view_count: 45 },
    ])
    .select();
  assert(!postsErr && posts?.length === 5, '4.2 Insert 5 posts', postsErr?.message);

  const publishedPostIds = posts?.filter(p => p.published).map(p => p.id) || [];
  const draftPostId = posts?.find(p => !p.published)?.id;

  // 4.3 Add comments
  const { error: commErr } = await alice
    .from('comments')
    .insert([
      { post_id: publishedPostIds[0], user_id: aliceId, body: 'Great article!' },
      { post_id: publishedPostIds[0], user_id: aliceId, body: 'Added more examples.' },
      { post_id: publishedPostIds[1], user_id: aliceId, body: 'RLS is essential.' },
    ]);
  assert(!commErr, '4.3 Insert comments', commErr?.message);

  // 4.4 Add bookmarks (private)
  const { error: bmErr } = await alice
    .from('bookmarks')
    .insert([
      { user_id: aliceId, post_id: publishedPostIds[0], note: 'review later' },
      { user_id: aliceId, post_id: publishedPostIds[1], note: 'security ref' },
    ]);
  assert(!bmErr, '4.4 Insert bookmarks (private)', bmErr?.message);

  // ──────────────────────────────────────
  section('5. Anonymous Access — Read Public, Blocked from Writing');
  // ──────────────────────────────────────

  // Create a fresh anon client (no session)
  const anon = createClient(API_URL, ANON_KEY);

  // 5.1 Anon can read published posts
  const { data: anonPosts, error: anonPostsErr } = await anon.from('posts').select('*');
  assert(!anonPostsErr, '5.1 Anon reads posts', anonPostsErr?.message);
  assert(anonPosts?.length >= 4, '5.2 Anon sees published posts (>= 4)');
  assert(!anonPosts?.some(p => !p.published), '5.3 Anon cannot see drafts');

  // 5.4 Anon can read profiles
  const { data: anonProf } = await anon.from('profiles').select('*');
  assert(anonProf?.length >= 1, '5.4 Anon reads profiles');

  // 5.5 Anon can read comments
  const { data: anonComm } = await anon.from('comments').select('*');
  assert(anonComm?.length >= 3, '5.5 Anon reads comments');

  // 5.6 Anon CANNOT read bookmarks (no policy for anon)
  const { data: anonBm, error: anonBmErr } = await anon.from('bookmarks').select('*');
  assert(anonBm?.length === 0 || anonBmErr, '5.6 Anon cannot read bookmarks');

  // 5.7 Anon CANNOT insert posts
  const { error: anonInsErr } = await anon.from('posts').insert({
    author_id: aliceId, title: 'Hacked', body: 'Should fail', published: true,
  });
  assert(!!anonInsErr, '5.7 Anon cannot insert posts');

  // 5.8 Anon CANNOT insert profiles
  const { error: anonProfInsErr } = await anon.from('profiles').insert({
    id: aliceId, username: 'hacker',
  });
  assert(!!anonProfInsErr, '5.8 Anon cannot insert profiles');

  // 5.9 Anon can call public RPC
  const { data: anonRpc, error: anonRpcErr } = await anon.rpc('search_posts', { keyword: 'Supabase' });
  assert(!anonRpcErr, '5.9 Anon calls RPC search_posts', anonRpcErr?.message);
  assert(anonRpc?.length >= 2, '5.10 RPC returns matching posts');

  // ──────────────────────────────────────
  section('6. Multi-User Isolation — Bob vs Alice');
  // ──────────────────────────────────────

  // Bob creates his own profile and data
  const { error: bobProfErr } = await bobClient
    .from('profiles')
    .insert({ id: bobId, username: 'bob', display_name: 'Bob Jones' })
    .select();
  assert(!bobProfErr, '6.1 Bob creates profile', bobProfErr?.message);

  // Bob creates a post
  const { data: bobPosts, error: bobPostErr } = await bobClient
    .from('posts')
    .insert([
      { author_id: bobId, title: "Bob's Guide to APIs", body: 'API design tips...', published: true, category: 'tutorial' },
      { author_id: bobId, title: "Bob's Secret Draft", body: 'Not published yet', published: false, category: 'news' },
    ])
    .select();
  assert(!bobPostErr && bobPosts?.length === 2, '6.2 Bob creates posts', bobPostErr?.message);
  const bobPublicPostId = bobPosts?.find(p => p.published)?.id;

  // Bob bookmarks Alice's post
  const { error: bobBmErr } = await bobClient
    .from('bookmarks')
    .insert({ user_id: bobId, post_id: publishedPostIds[0], note: "Alice's great post" });
  assert(!bobBmErr, '6.3 Bob bookmarks Alice post', bobBmErr?.message);

  // 6.4 Alice CANNOT see Bob's draft
  const { data: aliceSeesBob } = await alice.from('posts').select('*').eq('author_id', bobId);
  const aliceSeeBobDraft = aliceSeesBob?.some(p => !p.published);
  assert(!aliceSeeBobDraft, "6.4 Alice cannot see Bob's draft");
  assert(aliceSeesBob?.some(p => p.published), "6.5 Alice can see Bob's published post");

  // 6.6 Alice CANNOT see Bob's bookmarks
  const { data: aliceBm } = await alice.from('bookmarks').select('*');
  assert(aliceBm?.every(b => b.user_id === aliceId), "6.6 Alice only sees own bookmarks");

  // 6.7 Bob CANNOT see Alice's bookmarks
  const { data: bobBm } = await bobClient.from('bookmarks').select('*');
  assert(bobBm?.every(b => b.user_id === bobId), "6.7 Bob only sees own bookmarks");

  // 6.8 Alice CANNOT update Bob's post
  const { error: aliceUpdBob } = await alice
    .from('posts')
    .update({ title: 'Hacked by Alice' })
    .eq('id', bobPublicPostId);
  // Should either error or update 0 rows (RLS blocks)
  const { data: bobPostCheck } = await adminClient
    .from('posts').select('title').eq('id', bobPublicPostId).single();
  assert(bobPostCheck?.title !== 'Hacked by Alice', "6.8 Alice cannot update Bob's post");

  // 6.9 Alice CANNOT delete Bob's post
  const { error: aliceDelBob } = await alice
    .from('posts')
    .delete()
    .eq('id', bobPublicPostId);
  const { data: bobPostStill } = await adminClient
    .from('posts').select('id').eq('id', bobPublicPostId);
  assert(bobPostStill?.length === 1, "6.9 Alice cannot delete Bob's post");

  // 6.10 Service role sees EVERYTHING
  const { data: allPosts } = await adminClient.from('posts').select('*');
  const { data: allBookmarks } = await adminClient.from('bookmarks').select('*');
  assert(allPosts?.length >= 7, '6.10 Service role sees all posts (>= 7)');
  assert(allBookmarks?.length >= 3, '6.11 Service role sees all bookmarks (>= 3)');

  // ──────────────────────────────────────
  section('7. Advanced Queries — Filters, Ordering, Pagination');
  // ──────────────────────────────────────

  // 7.1 eq filter
  const { data: eqData } = await anon.from('posts').select('*').eq('category', 'tutorial');
  assert(eqData?.every(p => p.category === 'tutorial'), '7.1 eq filter');

  // 7.2 neq filter
  const { data: neqData } = await anon.from('posts').select('*').neq('category', 'tutorial');
  assert(neqData?.every(p => p.category !== 'tutorial'), '7.2 neq filter');

  // 7.3 gt filter
  const { data: gtData } = await anon.from('posts').select('*').gt('view_count', 100);
  assert(gtData?.every(p => p.view_count > 100), '7.3 gt filter');

  // 7.4 gte filter
  const { data: gteData } = await anon.from('posts').select('*').gte('view_count', 89);
  assert(gteData?.every(p => p.view_count >= 89), '7.4 gte filter');

  // 7.5 lt filter
  const { data: ltData } = await anon.from('posts').select('*').lt('view_count', 100);
  assert(ltData?.every(p => p.view_count < 100), '7.5 lt filter');

  // 7.6 lte filter
  const { data: lteData } = await anon.from('posts').select('*').lte('view_count', 89);
  assert(lteData?.every(p => p.view_count <= 89), '7.6 lte filter');

  // 7.7 like / ilike
  const { data: ilikeData } = await anon.from('posts').select('*').ilike('title', '%supabase%');
  assert(ilikeData?.length >= 2, '7.7 ilike filter');

  // 7.8 in filter
  const { data: inData } = await anon.from('posts').select('*').in('category', ['tutorial', 'opinion']);
  assert(inData?.every(p => ['tutorial', 'opinion'].includes(p.category)), '7.8 in filter');

  // 7.9 is filter (null check)
  const { data: isData } = await anon.from('profiles').select('*').is('avatar_url', null);
  assert(isData?.every(p => p.avatar_url === null), '7.9 is null filter');

  // 7.10 Order ascending
  const { data: ascData } = await anon.from('posts').select('view_count').order('view_count', { ascending: true });
  const ascVals = ascData?.map(r => r.view_count) || [];
  assert(ascVals.every((v, i) => i === 0 || v >= ascVals[i - 1]), '7.10 order ascending');

  // 7.11 Order descending
  const { data: descData } = await anon.from('posts').select('view_count').order('view_count', { ascending: false });
  const descVals = descData?.map(r => r.view_count) || [];
  assert(descVals.every((v, i) => i === 0 || v <= descVals[i - 1]), '7.11 order descending');

  // 7.12 Limit
  const { data: limData } = await anon.from('posts').select('*').limit(2);
  assert(limData?.length === 2, '7.12 limit(2)');

  // 7.13 Limit + offset (pagination)
  const { data: page1 } = await anon.from('posts').select('id').order('id').limit(2).range(0, 1);
  const { data: page2 } = await anon.from('posts').select('id').order('id').limit(2).range(2, 3);
  assert(page1?.length === 2 && page2?.length >= 1, '7.13 pagination range()');
  assert(page1?.[0]?.id !== page2?.[0]?.id, '7.14 pages return different data');

  // 7.15 Select specific columns
  const { data: colData } = await anon.from('posts').select('id, title').limit(1);
  assert(colData?.[0] && 'id' in colData[0] && 'title' in colData[0], '7.15 select specific columns');
  assert(!('body' in (colData?.[0] || {})), '7.16 unrequested columns excluded');

  // 7.17 Count (head: true)
  const { count, error: countErr } = await anon
    .from('posts')
    .select('*', { count: 'exact', head: true });
  assert(!countErr, '7.17 count query', countErr?.message);

  // 7.18 single() — single object response
  const { data: singleData, error: singleErr } = await anon
    .from('profiles')
    .select('*')
    .eq('username', 'alice')
    .single();
  assert(!singleErr && singleData?.username === 'alice', '7.18 .single() returns object', singleErr?.message);

  // 7.19 Upsert
  const { data: upsData, error: upsErr } = await alice
    .from('profiles')
    .upsert({ id: aliceId, username: 'alice', display_name: 'Alice S.', bio: 'Updated bio' })
    .select()
    .single();
  assert(!upsErr && upsData?.bio === 'Updated bio', '7.19 upsert updates existing', upsErr?.message);

  // 7.20 RPC with params
  const { data: statsData, error: statsErr } = await anon.rpc('get_post_stats', {
    target_post_id: publishedPostIds[0],
  });
  assert(!statsErr, '7.20 RPC get_post_stats', statsErr?.message);
  assert(statsData?.[0]?.comment_count >= 2, '7.21 RPC returns correct comment_count');

  // 7.22 RPC search
  const { data: searchData, error: searchErr } = await anon.rpc('search_posts', { keyword: 'PostgreSQL' });
  assert(!searchErr && searchData?.length >= 1, '7.22 RPC search_posts', searchErr?.message);

  // ──────────────────────────────────────
  section('8. Update & Delete — Own Data Only');
  // ──────────────────────────────────────

  // 8.1 Alice updates her own post
  const { data: updPost, error: updPostErr } = await alice
    .from('posts')
    .update({ title: 'Updated: Getting Started with Supabase', view_count: 200 })
    .eq('id', publishedPostIds[0])
    .select()
    .single();
  assert(!updPostErr && updPost?.title.startsWith('Updated'), '8.1 Update own post', updPostErr?.message);
  assert(updPost?.view_count === 200, '8.2 Update changes view_count');

  // 8.3 Alice updates her profile
  const { data: updProf, error: updProfErr } = await alice
    .from('profiles')
    .update({ bio: 'Senior dev at Acme' })
    .eq('id', aliceId)
    .select()
    .single();
  assert(!updProfErr && updProf?.bio === 'Senior dev at Acme', '8.3 Update own profile', updProfErr?.message);

  // 8.4 Alice deletes a bookmark
  const { data: delBm, error: delBmErr } = await alice
    .from('bookmarks')
    .delete()
    .eq('post_id', publishedPostIds[1])
    .select();
  assert(!delBmErr && delBm?.length === 1, '8.4 Delete own bookmark', delBmErr?.message);

  // 8.5 Verify bookmark is gone
  const { data: bmsAfter } = await alice.from('bookmarks').select('*');
  assert(bmsAfter?.length === 1, '8.5 Bookmark count decreased');

  // 8.6 Alice deletes her draft post
  const { data: delDraft, error: delDraftErr } = await alice
    .from('posts')
    .delete()
    .eq('id', draftPostId)
    .select();
  assert(!delDraftErr && delDraft?.[0]?.id === draftPostId, '8.6 Delete own draft', delDraftErr?.message);

  // ──────────────────────────────────────
  section('9. Auth Edge Cases');
  // ──────────────────────────────────────

  // 9.1 Sign out
  const { error: soErr } = await alice.auth.signOut();
  assert(!soErr, '9.1 signOut', soErr?.message);

  // 9.2 After sign out, getUser should fail
  const { data: { user: gone }, error: goneErr } = await alice.auth.getUser();
  assert(!gone || goneErr, '9.2 getUser fails after signOut');

  // 9.3 Sign back in with password
  const { data: si, error: siErr } = await alice.auth.signInWithPassword({
    email: user1Email, password: user1Pass,
  });
  assert(!siErr && si.session?.access_token, '9.3 signInWithPassword after signOut', siErr?.message);

  // 9.4 Change password
  const newPass = 'Alice$ecureNew789!';
  const { error: cpErr } = await alice.auth.updateUser({ password: newPass });
  assert(!cpErr, '9.4 updateUser password', cpErr?.message);

  // 9.5 Sign out and sign in with new password
  await alice.auth.signOut();
  const { data: si2, error: si2Err } = await alice.auth.signInWithPassword({
    email: user1Email, password: newPass,
  });
  assert(!si2Err && si2.session?.access_token, '9.5 signIn with new password', si2Err?.message);

  // 9.6 Wrong password
  const tmpClient = createClient(API_URL, ANON_KEY);
  const { error: wpErr } = await tmpClient.auth.signInWithPassword({
    email: user1Email, password: 'WrongPassword!',
  });
  assert(!!wpErr, '9.6 Wrong password returns error');

  // 9.7 Duplicate sign up
  const { error: dupErr } = await tmpClient.auth.signUp({
    email: user1Email, password: 'AnyPassword1!',
  });
  assert(!!dupErr, '9.7 Duplicate signUp returns error');

  // ──────────────────────────────────────
  section('10. Service Role — Admin Operations');
  // ──────────────────────────────────────

  // 10.1 Service role can read all data across users
  const { data: allProf } = await adminClient.from('profiles').select('*');
  assert(allProf?.length >= 2, '10.1 Service role reads all profiles');

  // 10.2 Service role can insert for any user
  const { data: adminIns, error: adminInsErr } = await adminClient
    .from('comments')
    .insert({ post_id: bobPublicPostId, user_id: aliceId, body: 'Admin-inserted comment' })
    .select();
  assert(!adminInsErr, '10.2 Service role inserts comment', adminInsErr?.message);

  // 10.3 Service role can update any row
  const { error: adminUpdErr } = await adminClient
    .from('posts')
    .update({ view_count: 999 })
    .eq('id', bobPublicPostId);
  assert(!adminUpdErr, '10.3 Service role updates any post', adminUpdErr?.message);

  // 10.4 Service role sees unpublished posts too
  const { data: adminAll } = await adminClient.from('posts').select('*');
  assert(adminAll?.some(p => !p.published), '10.4 Service role sees drafts');

  // 10.5 Service role can delete any bookmark
  const { data: adminDel, error: adminDelErr } = await adminClient
    .from('bookmarks')
    .delete()
    .eq('user_id', bobId)
    .select();
  assert(!adminDelErr && adminDel?.length >= 1, '10.5 Service role deletes bookmark', adminDelErr?.message);

  // ──────────────────────────────────────
  // RESULTS
  // ──────────────────────────────────────

  console.log(`\n\x1b[1m${'═'.repeat(50)}\x1b[0m`);
  console.log(`\x1b[1m  Results: \x1b[32m${passed} passed\x1b[0m, \x1b[31m${failed} failed\x1b[0m, \x1b[33m${skipped} skipped\x1b[0m`);
  console.log(`\x1b[1m${'═'.repeat(50)}\x1b[0m`);

  if (failures.length > 0) {
    console.log(`\n\x1b[31mFailures:\x1b[0m`);
    failures.forEach(f => console.log(`  - ${f}`));
  } else {
    console.log(`\n\x1b[32mAll tests passed! supabase-js works as a drop-in replacement.\x1b[0m`);
  }

  process.exit(failed > 0 ? 1 : 0);
}

run().catch(e => {
  console.error('Fatal error:', e);
  process.exit(1);
});
