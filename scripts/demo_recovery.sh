#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
CONTAINER="orchestrator-recovery-demo-$$-$RANDOM"
cleanup() {
  docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

command -v go >/dev/null || { echo "Go is required" >&2; exit 1; }
command -v docker >/dev/null || { echo "Docker is required" >&2; exit 1; }

# Use a disposable database, independent of application databases and credentials.
docker run -d --name "$CONTAINER" \
  -e POSTGRES_USER=orchestrator -e POSTGRES_PASSWORD=demo-local-only \
  -e POSTGRES_DB=recovery_demo -p 127.0.0.1::5432 postgres:16-alpine >/dev/null
ready=false
for _ in {1..60}; do
  if docker exec "$CONTAINER" pg_isready -U orchestrator -d recovery_demo >/dev/null 2>&1; then
    ready=true
    break
  fi
  sleep 0.25
done
if [[ "$ready" != true ]]; then
  echo "Demo PostgreSQL did not become ready" >&2
  exit 1
fi
port="$(docker port "$CONTAINER" 5432/tcp | head -1 | tr -d '\r' | awk -F: '{print $NF}')"
cd "$ROOT_DIR/backend"
ORCH_DATABASE_URL="postgres://orchestrator:demo-local-only@127.0.0.1:$port/recovery_demo?sslmode=disable" \
  go run ./cmd/recovery-demo
