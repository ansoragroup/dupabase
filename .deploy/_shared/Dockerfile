# Stage 1: Build Go binary
FROM golang:1.27.1-alpine AS go-builder

WORKDIR /app

# Cache dependencies
COPY go.mod go.sum ./
RUN go mod download

# Build binary
COPY . .
ARG BUILD_REVISION=development
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w -X github.com/ansoraGROUP/dupabase/internal/server.BuildRevision=${BUILD_REVISION}" -o server ./cmd/server

# Stage 2: Build Next.js dashboard
FROM node:24-alpine AS next-builder

WORKDIR /app

# Cache dependencies
COPY dashboard/package.json dashboard/package-lock.json ./
RUN npm ci

# Build dashboard with empty API URL (same-origin in production)
COPY dashboard/ .
ENV NEXT_PUBLIC_API_URL=""
RUN npm run build

# Stage 3: Production
# Use the supported Node.js LTS line in both build and runtime stages.
FROM node:24-alpine

# The standalone dashboard runs with node; package managers are build-only.
RUN rm -rf /usr/local/lib/node_modules/npm /opt/yarn-* \
    /usr/local/bin/npm /usr/local/bin/npx /usr/local/bin/yarn /usr/local/bin/yarnpkg

ARG PG_VERSION=16
RUN apk --no-cache add ca-certificates tzdata postgresql${PG_VERSION}-client

WORKDIR /app

# Go binary and migrations
COPY --from=go-builder /app/server .
COPY --from=go-builder /app/migrations ./migrations

# Next.js standalone output
COPY --from=next-builder /app/.next/standalone ./dashboard
COPY --from=next-builder /app/.next/static ./dashboard/.next/static
COPY --from=next-builder /app/public ./dashboard/public

# Entrypoint script
COPY .deploy/_shared/entrypoint.sh .
RUN chmod +x entrypoint.sh

# Ensure imports directory is writable
RUN mkdir -p /tmp/imports && chown node:node /tmp/imports

EXPOSE 3333

HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 \
  CMD wget -qO- http://127.0.0.1:3333/health || exit 1

USER node

CMD ["./entrypoint.sh"]
