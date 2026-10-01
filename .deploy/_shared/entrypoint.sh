#!/bin/sh
set -e

# Start Next.js dashboard in the background (port 3000 internal)
cd /app/dashboard
# The dashboard does not need the Go server's database or signing credentials.
env -i PATH="$PATH" HOME="$HOME" NODE_ENV=production HOSTNAME=127.0.0.1 PORT=3000 node server.js &
NEXT_PID=$!

# Wait for Next.js to be ready
echo "Waiting for Next.js to start..."
for i in $(seq 1 30); do
  if wget -q --spider http://127.0.0.1:3000 2>/dev/null; then
    echo "Next.js is ready"
    break
  fi
  if [ "$i" -eq 30 ]; then
    echo "WARNING: Next.js did not respond after 30 seconds, starting Go server anyway"
  fi
  sleep 1
done

# Start Go server (port 3333, proxies non-API routes to Next.js)
cd /app
export DASHBOARD_URL="http://127.0.0.1:3000"
./server &
GO_PID=$!

# Handle shutdown: kill both processes
shutdown() {
  kill "$NEXT_PID" "$GO_PID" 2>/dev/null || true
  wait "$NEXT_PID" "$GO_PID" 2>/dev/null || true
  exit 0
}
trap shutdown SIGTERM SIGINT

# Wait for either process to exit
while kill -0 "$GO_PID" 2>/dev/null && kill -0 "$NEXT_PID" 2>/dev/null; do
  sleep 1
done

# If one dies, stop the other
shutdown
