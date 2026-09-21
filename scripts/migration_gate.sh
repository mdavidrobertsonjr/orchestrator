#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DATABASE_URL="${ORCH_DATABASE_URL:-postgres://orchestrator:orchestrator@localhost:5432/orchestrator?sslmode=disable}"
PORT="${ORCH_MIGRATION_GATE_PORT:-18080}"

cd "${ROOT_DIR}/backend"
ORCH_DATABASE_URL="${DATABASE_URL}" go run ./cmd/migrate

ORCH_ADDR=":${PORT}" \
ORCH_DATABASE_URL="${DATABASE_URL}" \
ORCH_AUTO_MIGRATE=false \
ORCH_EMBEDDED_WORKERS=false \
ORCH_SCHEDULER_ENABLED=false \
go run ./cmd/api >/tmp/orchestrator-migration-gate.log 2>&1 &
api_pid=$!
cleanup() {
	kill "${api_pid}" 2>/dev/null || true
	wait "${api_pid}" 2>/dev/null || true
}
trap cleanup EXIT

for _ in {1..60}; do
	if curl -fsS "http://127.0.0.1:${PORT}/readyz" >/dev/null 2>&1; then
		echo "migration gate passed: API starts with automatic migrations disabled"
		exit 0
	fi
	sleep 1
done

echo "migration gate failed; API did not become ready" >&2
cat /tmp/orchestrator-migration-gate.log >&2 || true
exit 1
