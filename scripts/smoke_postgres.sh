#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
POSTGRES_URL="${ORCH_DATABASE_URL:-postgres://orchestrator:orchestrator@localhost:5432/orchestrator?sslmode=disable}"
GOCACHE="${GOCACHE:-/tmp/go-build-cache}"

cd "${ROOT_DIR}"
docker compose up -d postgres

for _ in {1..60}; do
  if docker compose exec -T postgres pg_isready -U orchestrator -d orchestrator >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
docker compose exec -T postgres pg_isready -U orchestrator -d orchestrator >/dev/null

cd backend
env GOCACHE="${GOCACHE}" ORCH_DATABASE_URL="${POSTGRES_URL}" go test -tags=integration ./internal/integration
