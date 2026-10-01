# Compatibility and controlled upgrades

Reviewed 2026-10-02. The public API stays on the existing routes and response
contracts. Dependency major versions do not automatically require a Dupabase
API major release: an implementation upgrade that preserves these contracts is
released as a minor or patch, with compatibility evidence.

## Tested matrix

| PostgreSQL server | Dump/restore client | SDK contract fixtures |
| --- | --- | --- |
| 14 | patched 16 | Supabase JS 1.35.7, 2.95.3, current locked 2.x |
| 15 | patched 16 | same |
| 16 | patched 16 | same |
| 17 | patched 17 | same |
| 18 | patched 18 | same |

The current lockfile contains Supabase JS 2.117.2. Version 2.95.3 is the previous
repository baseline. Version 1.35.7 tests legacy client compatibility; old SDKs
are installed only in disposable test directories and are not bundled into the
dashboard or production image. Current 2.x is the maintained dependency.

The container supplies clients 16.15+, 17.11+ and 18.6+ in their respective major
lines. Source builds require these clients in the standard Alpine, Debian or
Homebrew versioned directories, or under `POSTGRES_CLIENT_BIN_ROOT/<major>/`.
The platform and project databases belong to the same PostgreSQL cluster.
Client selection reads the server's connection metadata and does not add a
database query to normal REST requests.

These server majors are the current supported stable set according to the
[PostgreSQL versioning policy](https://www.postgresql.org/support/versioning/).
Preview majors enter the supported matrix after their stable release and a
successful compatibility run. Existing supported majors are deprecated with
notice and migration instructions; removing a supported contract requires a
Dupabase major release.

## Feature boundaries

The SDK contracts cover insert/select/update/delete/upsert, filters, projection,
ordering, pagination/count, JSONB and NULL values, table-returning RPC, RLS,
password signup/login, get/update user, refresh rotation and logout. Every server
major also runs the 98 API security contracts, including real custom export,
import, S3 backup/restore, cancellation, auth isolation and resource bounds.

This is a GoTrue/PostgREST compatibility layer. Supabase's services have separate
release lines, rather than one platform major that this application can certify.
Realtime transport, Storage APIs, Edge Functions, OAuth/SSO, phone auth and
managed email delivery are not implemented. Scalar RPC currently retains the
existing row-array response; full PostgREST scalar-response semantics require
explicit API negotiation rather than silently changing existing callers.
Extensions, custom collations and SQL features must exist on the destination
server and be tested with the application's own schema.

## Database upgrades

CI exports real archives from every supported major and tests all ten forward
pairs (14→15/16/17/18, 15→16/17/18, 16→17/18, 17→18). It verifies actual data,
password hashes and identities, enum/domain/generated columns, JSONB, functions
and RLS. This certifies the tested project archive path, not an automatic upgrade
of a running PostgreSQL data directory or a complete external Supabase cluster.

Before clean import or trigger changes, archive headers and target/client
versions are checked. A newer-server dump cannot be restored into an older
server; the rejection test proves existing data survives even with clean import
selected. The custom archive's drop/recreate/restore runs in one transaction.
The optional `clean_import` pre-step also removes public tables absent from the
archive and remains a separate destructive step; take a verified backup before
selecting it. Plain SQL remains a user script and may contain its own
transactions; take a verified backup first.

For a production PostgreSQL major upgrade:

1. Take a verified project backup and a cluster-level backup of platform data,
   roles and PostgreSQL configuration. Preserve application signing/encryption
   secrets, extensions and collation settings.
2. Restore to an isolated destination on the intended newer major. Test the
   application's schema and workloads, auth, RLS, backup restore, extensions and
   performance there. Follow the destination's official upgrade instructions.
3. Quiesce writes, take the final consistent backup and cut over only after
   verification. Keep the old cluster and immutable application image available
   for rollback. Account for writes after cutover: reverting an image does not
   reverse a database major upgrade or replay new data into the old cluster.

## Application and dependency upgrades

CI starts the v1.0.0 image, retains its keys, tokens, users and data, switches to
the candidate on the same database, then switches back. Both directions must
preserve the original API keys, JWTs, password login and RLS-protected data.
Migration batches use a transaction-scoped advisory lock and commit atomically.
Concurrent startup and failed-batch rollback are tested on each server major.

Dependabot covers Go, the SDK, dashboard, GitHub Actions and Docker build images.
Compatible minor/patch updates are grouped. Majors have separate PRs and must
pass the complete matrix, security checks and review; there is no blind auto
merge. Copied bcrypt/Blowfish are tracked with their upstream hashes, advisory
checks and a daily upstream drift check.

Node 24 LTS is the production/runtime baseline, with matching Node 24 types.
The dashboard moves to TypeScript 6, Vitest 5, jsdom 30, jest-dom 7, shadcn 4 and
lucide-react 1. ESLint 10 and TypeScript 7 remain gated because current Next.js
lint plugins declare incompatible peer ranges. Installing them with forced
peer overrides is not a supported upgrade. Dependabot keeps those proposals
visible for a future compatible transition.

## Performance and release evidence

The candidate and v1.0.0 are built on the same architecture and tested against
separate, equivalent databases on the same host. Read and filtered-write bursts
use eight concurrent clients behind an explicitly trusted disposable gateway,
with normal rate limits and at most 30 requests per client address per burst.
Five alternating trials of 2,400 requests follow warmup. Median
throughput must retain at least 75% of the baseline and median p95 latency must
remain within 1.5×. The report contains every trial and ratio. This is a release
regression check, not a production capacity promise; validate real workloads and
resource budgets before a database or infrastructure cutover.

Tag releases publish a checked image, digest, SBOM, vulnerability results,
compatibility/performance report and SHA-256 checksums. Existing releases are
immutable. Production publication scans the exact image it pushes; deployment
must report the expected revision. Production workflows run sequentially to
prevent concurrent rollouts. Daily security checks also inspect the published
production image so a patched candidate does not hide an outdated deployment.
