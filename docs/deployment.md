# Deployment Guide

Dupabase is a single Go binary that connects to your PostgreSQL. This guide covers deploying to a Hetzner VPS (cheapest option), Docker, or bare metal.

## Hetzner VPS

The most cost-effective way to run Dupabase. A **CPX22** (2 vCPU, 4 GB RAM, 80 GB NVMe, 20 TB traffic) costs **$7.59/month** and comfortably runs Dupabase + PostgreSQL with dozens of projects.

For comparison: a single Supabase Pro + Medium compute project costs $75/month. On Hetzner, you get unlimited projects for $7.59/month total.

### 1. Create the server

1. Sign up at [hetzner.com](https://www.hetzner.com/cloud)
2. Create a Cloud Server:
   - **Location**: Pick the closest datacenter
   - **Image**: Ubuntu 24.04
   - **Type**: CPX22 ($7.59/mo) for small setups, CPX32 ($12.59/mo) for medium, CPX42 ($22.59/mo) for heavy use
   - **SSH Key**: Add your public key
3. Note the server IP address

### 2. Install PostgreSQL

```bash
ssh root@YOUR_SERVER_IP

# Install PostgreSQL 16
apt update && apt install -y postgresql-16

# Create a database user for Dupabase
sudo -u postgres psql -c "CREATE USER dupabase WITH PASSWORD 'your-secure-password' CREATEROLE CREATEDB;"
sudo -u postgres psql -c "CREATE DATABASE dupabase OWNER dupabase;"

# Allow local connections (default is fine for same-server setup)
```

### 3. Install Docker

```bash
curl -fsSL https://get.docker.com | sh
```

### 4. Deploy Dupabase

```bash
# Clone the repository
git clone https://github.com/ansoraGROUP/dupabase.git
cd dupabase

# Configure production environment
cp .deploy/prod/.env.example .deploy/prod/.env
```

Edit `.deploy/prod/.env`:

```bash
DATABASE_URL=postgresql://dupabase:your-secure-password@host.docker.internal:5432/dupabase
PLATFORM_JWT_SECRET=$(openssl rand -base64 48)
SITE_URL=https://your-domain.com
ADMIN_EMAIL=admin@your-domain.com
ADMIN_PASSWORD=your-admin-password
ALLOWED_ORIGINS=https://your-domain.com
```

Encrypt and commit (so CI/CD can deploy):

```bash
make encrypt ENV=prod
git add .deploy/prod/.env.encrypted
git commit -m "Add encrypted production env"
```

Build and start:

```bash
make prod-build && make prod-up
```

Dupabase is now running on port 3333 (internal). Next, set up a reverse proxy for HTTPS.

### 5. Set up nginx + SSL

```bash
apt install -y nginx certbot python3-certbot-nginx
```

Create `/etc/nginx/sites-available/dupabase`:

```nginx
server {
    server_name your-domain.com;

    location / {
        proxy_pass http://127.0.0.1:3333;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;

        # Support large database imports
        client_max_body_size 500M;
    }
}
```

Enable and get SSL:

```bash
ln -s /etc/nginx/sites-available/dupabase /etc/nginx/sites-enabled/
nginx -t && systemctl reload nginx
certbot --nginx -d your-domain.com
```

Your Dupabase is now live at `https://your-domain.com` with automatic SSL renewal.

### 6. Firewall

```bash
ufw allow 22/tcp    # SSH
ufw allow 80/tcp    # HTTP (for certbot)
ufw allow 443/tcp   # HTTPS
ufw enable
```

PostgreSQL (port 5432) is **not** exposed to the internet — it's only accessible locally.

---

## Docker Deployment

For any server with Docker installed.

### Using the included docker-compose

The local docker-compose reads all configuration from `.deploy/local/.env`:

```bash
cd .deploy/local
cp .env.example .env
# Edit .env — set DATABASE_URL, PLATFORM_JWT_SECRET, etc.
```

Then build and run:

```bash
make build && make up
```

### Custom docker-compose.yaml

If you want to write your own compose file:

```yaml
services:
  dupabase:
    build:
      context: .
      dockerfile: .deploy/_shared/Dockerfile
    container_name: dupabase
    restart: unless-stopped
    ports:
      - "${DUPABASE_PORT:-3333}:3333"
    env_file:
      - .env
    # If PostgreSQL is on the host machine:
    extra_hosts:
      - "host.docker.internal:host-gateway"
```

```bash
docker compose up -d
```

### Pre-built image

```bash
# Build locally
docker build -f .deploy/_shared/Dockerfile -t dupabase .
docker run -d --name dupabase \
  -p 3333:3333 \
  --env-file .env \
  dupabase
```

---

## Manual Deployment (No Docker)

### Build

```bash
go build -ldflags="-s -w" -o dupabase ./cmd/server
```

This produces a single static binary (~15 MB).

### systemd Service

Create `/etc/systemd/system/dupabase.service`:

```ini
[Unit]
Description=Dupabase Server
After=postgresql.service network.target

[Service]
Type=simple
User=dupabase
WorkingDirectory=/opt/dupabase
ExecStart=/opt/dupabase/dupabase
EnvironmentFile=/opt/dupabase/.env
Restart=always
RestartSec=5

# Security hardening
NoNewPrivileges=yes
ProtectSystem=strict
ReadWritePaths=/tmp/imports

[Install]
WantedBy=multi-user.target
```

```bash
# Create service user
useradd -r -s /bin/false dupabase

# Deploy binary
cp dupabase /opt/dupabase/
cp .env /opt/dupabase/
cp -r migrations /opt/dupabase/
chown -R dupabase:dupabase /opt/dupabase

# Enable and start
systemctl daemon-reload
systemctl enable --now dupabase
systemctl status dupabase
```

---

## Caddy (Alternative to nginx)

If you prefer automatic SSL without certbot:

```
your-domain.com {
    reverse_proxy 127.0.0.1:3333
}
```

```bash
apt install caddy
# Edit /etc/caddy/Caddyfile with the above
systemctl reload caddy
```

Caddy automatically obtains and renews SSL certificates.

---

## Security Checklist

- [ ] PostgreSQL only listens on localhost (not exposed to internet)
- [ ] Firewall enabled (only 22, 80, 443 open)
- [ ] `PLATFORM_JWT_SECRET` is random, 48+ characters
- [ ] `ADMIN_PASSWORD` is strong
- [ ] HTTPS enabled with valid SSL certificate
- [ ] `ALLOWED_ORIGINS` set to your actual domain(s)
- [ ] `HOST=127.0.0.1` (only accept connections from reverse proxy)
- [ ] Regular PostgreSQL backups configured (see [Backup & Import](backup-and-import.md))

## CI/CD with GitHub Actions + Watchtower

Dupabase uses GitHub Actions to build and push Docker images to GHCR, then triggers [Watchtower](https://containrrr.dev/watchtower/) to auto-update the running container.

### How it works

1. Push to `main` → GitHub Actions builds the image and pushes to GHCR
2. Actions sends a curl request to Watchtower on your server
3. Watchtower pulls the new image and recreates the container

### Server setup

On your production server, set up Watchtower:

```yaml
# watchtower docker-compose.yaml
services:
  watchtower:
    image: containrrr/watchtower
    container_name: watchtower
    environment:
      - WATCHTOWER_HTTP_API_TOKEN=your-watchtower-token
      - WATCHTOWER_HTTP_API_UPDATE=true
      - WATCHTOWER_CLEANUP=true
      - WATCHTOWER_LABEL_ENABLE=true
    ports:
      - "8080:8080"
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - ~/.docker/config.json:/config.json:ro
    command: --http-api-update --label-enable
    restart: unless-stopped
```

Then configure and start Dupabase:

```bash
git clone https://github.com/ansoraGROUP/dupabase.git
cd dupabase
cp .deploy/prod/.env.example .deploy/prod/.env
# Edit .deploy/prod/.env with your values
docker compose -f .deploy/prod/docker-compose.yaml up -d
```

### GitHub secrets

Add these in Settings → Secrets → Actions:

| Secret | Description |
|--------|-------------|
| `WATCHTOWER_ENDPOINT` | Watchtower endpoint (e.g., `http://your-server:8080`) |
| `WATCHTOWER_TOKEN` | Watchtower HTTP API token |
| `DEPLOY_HEALTH_URL` | Optional health endpoint; defaults to `https://api.dupabase.dev/health` |

`GITHUB_TOKEN` is provided automatically for GHCR access.

### Encrypt / Decrypt env files

```bash
# Encrypt
ENCRYPTION_KEY=your-secret make encrypt ENV=prod

# Decrypt
ENCRYPTION_KEY=your-secret make decrypt ENV=prod
```

---

## Updating Dupabase

With Watchtower, updates are automatic on push to `main`.

For manual updates:

```bash
docker compose -f .deploy/prod/docker-compose.yaml pull
docker compose -f .deploy/prod/docker-compose.yaml up -d
```

Or without Docker:

```bash
git pull
go build -ldflags="-s -w" -o dupabase ./cmd/server
systemctl restart dupabase
```

Migrations run automatically on startup — no manual migration steps needed.
