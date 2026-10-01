# Dependency and security maintenance, 2026-10-01–02

The original audit revision was `b9c84b0f685a4284b508033fde906e92a45dbe1d`. Initial verification used local, uncommitted changes without publishing an image or deploying production. Subsequent commit/release work follows the authorized GitHub release pipeline. Existing HTTP routes, request formats, project keys and response field names are retained. Security checks now enforce the intended permissions and account policies, so requests previously succeeding through a bypass are rejected.

The Codex Security source audit conserved 30 investigator candidates and grouped them into 25 findings: one critical, six high, fifteen medium and three low. It reviewed 170 tracked source/text/lock-metadata files; the remaining favicon received passive container inspection. Findings describe the original source, with deployment-dependent prerequisites stated explicitly. The retained scan includes source excerpts, counterevidence and the original investigator receipts. Third-party implementations and live production configuration are outside that source audit.

## Dependency updates

| Area | Selected versions |
| --- | --- |
| Go | Toolchain 1.27.1; module minimum 1.26.0; pgx 5.11.0; jwt/v5 5.3.1; x/text 0.42.0; standard PBKDF2 and exact upstream bcrypt/Blowfish subset |
| AWS | Core SDK 1.47.1; S3 1.114.0; current `feature/s3/transfermanager` 0.4.12 for bounded multipart streaming |
| Dashboard runtime | Next.js 16.3.8; React/React DOM 19.3.0; Monaco 0.57.0 with DOMPurify override 3.4.16 |
| Compatibility client | Supabase JS 2.117.2 |
| Container | Go 1.27.1 Alpine builder and Node 24 Alpine build/runtime; PostgreSQL client stays on major 16 |

All used Go runtime dependencies were updated to their available compatible versions; unused upstream test/tool graph entries were not added as artificial direct dependencies. Npm direct and transitive updates are locked in both lockfiles. Both npm audits report zero vulnerabilities. Go vulnerability scanning now reports zero affected modules, imported packages and callable symbols. The unused OpenPGP umbrella dependency was removed from the module/import graph and compiled binary metadata. The exact BSD-licensed Go Authors' bcrypt/Blowfish source subset, upstream tests, provenance and file hashes are preserved under `internal/cryptocompat`; PBKDF2 uses the standard implementation the old external package already wrapped. Frozen legacy bcrypt/ciphertext fixtures and an independent review confirm compatibility. `scripts/update_crypto.py` reproduces/verifies the copied sources; `scripts/check_crypto_advisories.py` checks their actual upstream package paths against the official Go vulnerability database. The scheduled security workflow monitors both advisories and relevant upstream source drift.

Some releases were deliberately held to preserve a working compatible toolchain:

| Dependency | Retained | Reason |
| --- | --- | --- |
| ESLint | 9.39.5 | Next.js tooling/plugins do not support ESLint 10 in the tested setup |
| TypeScript | 5.9.3 | TypeScript 7 is a separate major migration |
| Vitest / jsdom | 4.1.11 / 28.1.0 | New majors require separate testing and migration |
| shadcn / lucide-react | 3.8.5 / 0.577.0 | Avoid generator/icon API changes unrelated to maintenance |
| testing-library/jest-dom | 6.9.1 | The attempted 6.10 release broke the matcher export and was deprecated |
| GitHub Actions | Latest selected current majors, pinned to immutable SHAs | Newer action majors require a self-hosted runner version that was not verified |

## Security and functional changes

- Project SQL, REST and PostgreSQL import/export/backup/restore workers use an actual confined `dp_…` database login. The operator connection is reserved for provisioning, migrations and ownership changes. SQL editor sessions are discarded after use. Every REST role, including `service_role`, is selected within its transaction.
- Standard Supabase roles remain available. Custom signed API roles remain accepted when the operator explicitly grants that role to the confined project login; pool recreation preserves those operator grants. PostgreSQL rejects ungranted roles. Tenant SQL cannot create or grant cluster roles.
- Viewer project responses retain their fields but redact service-role keys and signing secrets. Direct table-browser operations reject protected schemas. Table metadata exposes actual single-column unique/primary identities; single-row writes must affect exactly one row and roll back otherwise.
- Signup and password defaults now reach project creation. Confirmation, replacement-email confirmation, bans and live user eligibility are enforced. Anonymous refresh retains anonymous claims. Refresh tokens and platform invitations are consumed atomically, targeted invitations enforce their recipient, and login lockout is scoped by project.
- Admin bootstrap verifies the configured password before promoting an existing non-admin account. Account deletion removes private workspaces and its project databases/logins, keeps consumed invitations unusable, and rejects shared-owner deletion before touching project resources. Legacy admin test cleanup deletes only its own fixtures and restores registration mode using the administrator token.
- Plain SQL import uses a fresh restricted-mode key and a minimal worker environment; uploaded shell commands fail. SQL/COPY filtering preserves public data and RLS. Custom archive filtering distinguishes a schema from a public object named `auth`. Command output and concurrent jobs are bounded. Cancellation retains its terminal status and restores user triggers.
- Export exposes dump-process failures instead of returning an empty successful download. Database backups wait for dump success, use bounded streaming multipart uploads, and have collision-resistant object keys.
- Historical restore uses the current organization's credentials. New backup history records bind original endpoint/region/bucket and content SHA-256, checked before restoration. Internal S3 access requires an exact operator-approved origin; address checks occur again at connection time, with redirects and environment proxies blocked.
- Dashboard paths and row query values are encoded. SQL history is scoped by account/project in session storage and cleared at authentication boundaries. Stale authentication requests cannot restore logged-out state. Shared-organization backup toggling and project settings now address the selected organization/project.
- Dashboard lint/type issues are fixed. The container binds its internal dashboard consistently to IPv4 loopback and keeps Go application secrets out of the Next.js subprocess environment.
- CI checks Go vet/race/database compatibility on PostgreSQL 14–18 and dashboard lint/types/tests/build. The separate daily/PR security workflow checks Go/npm advisories, copied crypto provenance/advisories, CodeQL and the full production image. Actions are pinned. Deploy/release publication depends on these checks. Watchtower update failures fail deployment, and verification checks the expected additive `/health` revision. `WATCHTOWER_ENDPOINT` is documented consistently; `DEPLOY_HEALTH_URL` optionally overrides the health URL.
- Dependabot opens weekly updates for Go, both npm projects and GitHub Actions. Minor/patch updates are grouped; major releases stay in separate PRs for compatibility review. Tag releases include the verified image digest, CycloneDX inventory, vulnerability scan and SHA-256 checksums, and reject overwriting an existing GitHub release.

## Database and deployment compatibility

The existing platform migration mechanism runs additive migration `010_backup_scope_and_provenance.sql` on startup. It removes the user-only uniqueness constraint on backup settings, adds an organization lookup index and adds nullable backup provenance/digest columns. Existing backup-setting/history rows are preserved. The newest settings row per organization controls reads and scheduling; an older enabled duplicate cannot silently reactivate scheduling.

Project login/ownership upgrades happen lazily when a project pool is opened, and during new project creation. Ownership transfer preserves application tables, data, enum/domain/composite types, routines and RLS. The original account PostgreSQL login inherits its project ownership role so existing direct connection credentials still work. Extension-owned objects are excluded. The operator needs sufficient provisioning/ownership privileges and the existing global API roles; ordinary tenant execution uses the confined login.

Before rollout, configure `BACKUP_ALLOWED_ENDPOINTS` for private MinIO/S3-compatible origins and `TRUSTED_PROXY_CIDRS` for the actual proxy peers when `TRUST_PROXY=true`. Review `REST_MAX_RESPONSE_BYTES` for workloads needing larger pages. These variables are documented in root and deployment environment examples. No project API keys were automatically rotated.

Legacy backups have no retroactive checksum or immutable destination binding. Their restores fall back to current organization storage settings and lack the new end-to-end digest guarantee. New backups carry both provenance and a digest. Auto-confirmation-disabled projects need a separately supplied confirmation workflow; no email-delivery provider was added. Custom API-role grants need to be applied explicitly to the confined login during rollout.

## Verification

Validation used only disposable loopback fixtures and an isolated Docker network. Go race tests passed across all nine packages with PostgreSQL compatibility and S3 multipart integration enabled; later affected packages were rechecked after changes. Vet and module verification passed. Dashboard lint, TypeScript checks, 24 Vitest tests and the production Next.js build passed. The combined container was rebuilt and recreated for runtime checks. The API/Supabase SDK suite passed 98 checks, and the admin suite passed 78 checks; a seeded invitation survived cleanup and registration mode was restored.

The API/Supabase SDK suite verifies real tenant logins, cluster-privilege denial, RLS CRUD, viewer redaction, confirmation and ban behavior, token/invitation races, plain/custom imports, COPY preservation, psql shell rejection, cancellation/trigger restoration, response-size limits and an actual S3 backup/data-restore round-trip. Additional admin checks cover private-account/project deletion, protection of shared organizations, consumed invitation reuse, and preservation of a pre-existing invitation during test cleanup.

The first container advisory scan found 19 advisory occurrences in the base image's unused global npm tools. The production stage removes npm and Yarn; the dashboard build stage retains npm. The later crypto fix also removed the unused OpenPGP module warning. The latest Trivy 0.75.0 scan found zero Alpine, Node.js and Go advisories. This image uses Node 24.21.0, built-in Undici 7.29.1 and PostgreSQL client 16.15, with `/health` revision `maintenance-20261002-crypto`.

The updated local image tag is `dupabase:maintenance-20261002`, with image ID `sha256:c6d40a883c71cf73aa55c6efa4ce28c1cbabb1aa0ab47f78677a9c79ac0142cb`. Raw initial/final source-audit verification is retained with the original Codex Security report; the subsequent crypto fix has a separate retained verification receipt so the sealed original scan remains unchanged.

Native Chrome smoke testing verified fixture sign-in, project/table pages, a read-only Monaco SQL query under the confined login, rejection of a crafted invitation token, and sign-out. A 12 MB streaming fixture exercised multipart S3 upload and byte-for-byte readback. The S3-compatible fixture does not prove behavior against every production S3/MinIO deployment; TLS negotiation against a remote PostgreSQL server and the GitHub workflows were not executed.

No complete-compatibility claim is made for untested custom extensions or external clients. The executable local regression suite is `tests/security_regression.mjs`; it requires explicit disposable fixture settings and refuses remote targets. Go integration tests similarly require an explicit loopback database named `dupabase_maintenance_*`.

The three disposable fixture containers, their network and the fixture's exclusive PostgreSQL volume were removed after verification. The four pre-existing containers were preserved. The tested local image remains available for review.
