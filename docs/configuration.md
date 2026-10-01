# Configuration

All configuration is via environment variables.

- **Docker**: Copy `.deploy/local/.env.example` to `.deploy/local/.env` and edit.
- **Local (no Docker)**: Copy `.env.example` to `.env` and edit.

## Environment Variables

### Required

| Variable | Default | Description |
|----------|---------|-------------|
| `DATABASE_URL` | — | Connection string to **your** PostgreSQL. Must have `CREATEROLE` + `CREATEDB` privileges. Example: `postgresql://user:pass@localhost:5432/postgres` |
| `PLATFORM_JWT_SECRET` | — | Secret for dashboard JWT tokens. **Minimum 32 characters.** |

### Server

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `3000` | HTTP server port |
| `HOST` | `0.0.0.0` | Bind address |
| `SITE_URL` | `http://localhost:3000` | Public URL (used for callbacks) |

### Admin User

| Variable | Default | Description |
|----------|---------|-------------|
| `ADMIN_EMAIL` | — | Auto-create admin account on startup. Optional but recommended. |
| `ADMIN_PASSWORD` | — | Admin password. **Minimum 8 characters.** Required if `ADMIN_EMAIL` is set. |

The admin user is created idempotently on every startup — safe to leave in your env permanently.

### Connection Pooling

| Variable | Default | Description |
|----------|---------|-------------|
| `MAX_CONNECTIONS_PER_DB` | `5` | Max pool connections per project database |
| `GLOBAL_MAX_CONNECTIONS` | `100` | Total connection limit across all projects |
| `POOL_IDLE_TIMEOUT_SECONDS` | `300` | Close idle connections after this many seconds |

Dupabase uses an LRU connection pool — when `GLOBAL_MAX_CONNECTIONS` is reached, the least recently used project pool is evicted.

### Default Auth Settings

These apply when creating new projects. Each project can override them in the dashboard.

| Variable | Default | Description |
|----------|---------|-------------|
| `DEFAULT_ENABLE_SIGNUP` | `true` | Allow public signups |
| `DEFAULT_AUTOCONFIRM` | `true` | Auto-confirm new users (no email verification) |
| `DEFAULT_PASSWORD_MIN_LENGTH` | `6` | Minimum password length, from 6 to 72 |

### Security

| Variable | Default | Description |
|----------|---------|-------------|
| `BACKUP_ENCRYPTION_KEY` | Falls back to `PLATFORM_JWT_SECRET` | AES-256-GCM key for encrypting S3 backup credentials at rest |
| `ALLOWED_ORIGINS` | `localhost:3000,3001,3333` | CORS allowed origins (comma-separated) |
| `TRUST_PROXY` | `false` | Accept forwarded client addresses only from configured trusted peers |
| `TRUSTED_PROXY_CIDRS` | `127.0.0.0/8,::1/128` | Comma-separated proxy networks; set your actual proxy addresses before enabling proxy trust |
| `BACKUP_ALLOWED_ENDPOINTS` | Empty | Exact comma-separated HTTP(S) origins approved for private/internal S3-compatible storage, such as `http://minio:9000` |
| `REST_MAX_RESPONSE_BYTES` | `33554432` | Maximum encoded REST result size; oversized results return an error asking for a smaller page |

Private S3 endpoints require an operator-approved origin. Public endpoints must resolve to public addresses; redirects are blocked. PostgreSQL command-line workers preserve connection TLS options from `DATABASE_URL`, including verification/certificate settings.

When auto-confirmation is disabled, unconfirmed users receive no session and cannot log in until confirmed. Email delivery/verification workflows must be supplied separately. PostgreSQL workers require patched clients 16.15+, 17.11+ or 18.6+; the container supplies all three and selects automatically. Native installations can use `POSTGRES_CLIENT_BIN_ROOT/<major>/<tool>`. See the [compatibility matrix](compatibility.md).

### Import

| Variable | Default | Description |
|----------|---------|-------------|
| `IMPORT_MAX_SIZE_MB` | `500` | Maximum upload size for database imports |
| `IMPORT_TEMP_DIR` | `/tmp/imports` | Temporary directory for import file processing |

## Example Configurations

### Development

```bash
PORT=3333
DATABASE_URL=postgresql://postgres:postgres@localhost:5432/postgres
PLATFORM_JWT_SECRET=dev-secret-key-at-least-32-characters
ADMIN_EMAIL=admin@dev.local
ADMIN_PASSWORD=devpassword
```

### Staging (Hetzner VPS)

```bash
PORT=3333
HOST=0.0.0.0
SITE_URL=https://staging.example.com
DATABASE_URL=postgresql://dupabase:securepass@localhost:5432/dupabase
PLATFORM_JWT_SECRET=staging-jwt-secret-keep-this-very-long-and-random
ADMIN_EMAIL=admin@example.com
ADMIN_PASSWORD=staging-admin-password
ALLOWED_ORIGINS=https://staging.example.com
```

### Production

```bash
PORT=3333
HOST=127.0.0.1
SITE_URL=https://api.example.com
DATABASE_URL=postgresql://dupabase:S0cr%40t123@localhost:5432/dupabase
PLATFORM_JWT_SECRET=production-secret-generate-with-openssl-rand-base64-48
BACKUP_ENCRYPTION_KEY=separate-backup-key-generate-with-openssl-rand-base64-48
ADMIN_EMAIL=admin@example.com
ADMIN_PASSWORD=very-secure-admin-password
ALLOWED_ORIGINS=https://app.example.com,https://admin.example.com
MAX_CONNECTIONS_PER_DB=10
GLOBAL_MAX_CONNECTIONS=200
```

## Common Gotchas

### Password URL encoding

If your PostgreSQL password contains special characters, they must be URL-encoded in `DATABASE_URL`:

| Character | Encoded |
|-----------|---------|
| `@` | `%40` |
| `#` | `%23` |
| `?` | `%3F` |
| `/` | `%2F` |
| `:` | `%3A` |

Example: password `S0cr@t123` becomes `S0cr%40t123` in the connection string.

### JWT secret length

`PLATFORM_JWT_SECRET` must be at least 32 characters. The server will refuse to start if it's shorter. Generate a secure one:

```bash
openssl rand -base64 48
```
