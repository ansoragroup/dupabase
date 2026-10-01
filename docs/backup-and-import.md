# Backup & Import

## S3 Backups

Dupabase can automatically back up your project databases to any S3-compatible storage.

### Supported Providers

- **AWS S3**
- **Cloudflare R2**
- **MinIO** (self-hosted)
- **DigitalOcean Spaces**
- **Backblaze B2**
- Any S3-compatible API

### Setup

1. Go to the dashboard → **Backups** page
2. Enter your S3 credentials:
   - **Endpoint** — S3 API URL (e.g., `s3.amazonaws.com`, `your-minio.com:9000`)
   - **Bucket** — Bucket name
   - **Region** — AWS region (e.g., `us-east-1`)
   - **Access Key** — S3 access key
   - **Secret Key** — S3 secret key

> Your S3 credentials are encrypted at rest with AES-256-GCM. The encryption key is derived from `BACKUP_ENCRYPTION_KEY` (or falls back to `PLATFORM_JWT_SECRET`).

3. Configure the schedule:
   - **Frequency**: Hourly, Daily, or Weekly
   - **Retention**: Number of days to keep backups (older ones are auto-deleted)
   - **Projects**: Select "All projects" or pick specific ones

4. Save. Backups will run automatically on schedule.

### Manual Backup

Click **"Run Backup Now"** on the Backups page to trigger an immediate backup outside the schedule.

### Backup History

The Backups page shows a history of all backups with:
- Timestamp
- Status (success / failed)
- File size
- Projects included

### What Gets Backed Up

Each backup is a `pg_dump --format=custom` of the selected project databases. This includes:
- All tables and data
- Auth schema (users, sessions, tokens)
- Functions, triggers, RLS policies
- Indexes and constraints

### Restore

To restore from a backup, download the dump file from S3 and use `pg_restore`:

```bash
pg_restore \
  --host=localhost --port=5432 \
  --username=proj_abc123 \
  --dbname=proj_abc123 \
  --clean --if-exists \
  --no-owner --no-privileges \
  backup_file.dump
```

---

## Database Import

Import existing databases into Dupabase projects via pg_dump files.

### Supported Formats

| Format | Extension | Notes |
|--------|-----------|-------|
| Custom | `.dump`, `.backup` | Recommended — smaller files, selective restore |
| Plain SQL | `.sql` | Larger but human-readable |

### Size Limits

Default maximum: **500 MB** (configurable via `IMPORT_MAX_SIZE_MB` env var).

### How to Import

#### Via Dashboard (recommended)

1. Go to your project detail page
2. Click the **"Import"** tab
3. **Drag and drop** your `.dump` or `.sql` file (or click to browse)
4. Import starts automatically in the background
5. Check progress in the **Import History** section below

#### Via pg_restore (direct)

```bash
pg_restore \
  --host=localhost --port=5432 \
  --username=proj_abc123 \
  --dbname=proj_abc123 \
  --clean --if-exists \
  --no-owner --no-privileges \
  your_database.dump
```

### Auth Schema Filtering

When importing, Dupabase automatically filters out Supabase-specific auth schema objects that would conflict with Dupabase's own auth system. This includes:

- Supabase auth extensions (`pgjwt`, `pgcrypto` setup)
- Auth schema grants and permissions
- Supabase-specific functions and triggers in the auth schema

With **Skip Auth Schema** selected, auth schema definitions and auth table data
are excluded. The separate **Migrate Auth Users** option can import supported
user records from a plain Supabase SQL dump while retaining password hashes.
For Dupabase-to-Dupabase backups, restore the complete auth schema and data with
filtering disabled. Test the migration against the [compatibility matrix](compatibility.md).

### Import History

Each import is tracked with:
- Filename
- File size
- Status: `pending` → `running` → `completed` / `failed`
- Error message (if failed)
- Timestamp

### Common Import Issues

**"pg_restore: error: could not execute query"**
- Usually caused by objects that already exist. Use `--clean --if-exists` flags.

**File too large**
- Increase `IMPORT_MAX_SIZE_MB` in your `.env` file.

**Dollar-quoted functions fail**
- This can happen with plain SQL imports that have complex function bodies. Try using custom format (`.dump`) instead.

**PostgreSQL 18 psql commands**
- Patched PostgreSQL clients emit restricted-mode commands. Dupabase uses its
  own fresh restriction key for uploaded SQL and preserves public COPY/RLS data.
  Native clients must be patched; keep the protection when restoring manually.

**Dump/server version mismatch**
- The target must be the same major or newer than the source. Dupabase checks
  archive/client versions before clean import. PostgreSQL 14–16 use client 16;
  PostgreSQL 17/18 use matching clients. A PostgreSQL server downgrade is not an
  application-image rollback. See [controlled upgrades](compatibility.md).
