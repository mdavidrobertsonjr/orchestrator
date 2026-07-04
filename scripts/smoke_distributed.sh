#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
API_ADDR="${ORCH_ADDR:-:18080}"
API_PORT="${API_ADDR##*:}"
API_URL="http://localhost:${API_PORT}"
GOCACHE="${GOCACHE:-/tmp/go-build-cache}"
SMOKE_DB=""
if [[ -n "${ORCH_DATABASE_URL:-}" ]]; then
  POSTGRES_URL="${ORCH_DATABASE_URL}"
else
  SMOKE_DB="orchestrator_smoke_$$_${RANDOM}"
  POSTGRES_URL="postgres://orchestrator:orchestrator@localhost:5432/${SMOKE_DB}?sslmode=disable"
fi

if ! command -v docker >/dev/null 2>&1; then
  echo "docker is required for distributed smoke tests" >&2
  exit 1
fi

cleanup() {
  if [[ -n "${API_PID:-}" ]]; then
    kill "${API_PID}" 2>/dev/null || true
  fi
  if [[ -n "${WORKER_PID:-}" ]]; then
    kill "${WORKER_PID}" 2>/dev/null || true
  fi
  if [[ -n "${SMOKE_DB}" ]]; then
    docker compose exec -T postgres dropdb -U orchestrator --if-exists "${SMOKE_DB}" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT INT TERM

cd "${ROOT_DIR}"
docker compose up -d postgres
if [[ -n "${SMOKE_DB}" ]]; then
  docker compose exec -T postgres createdb -U orchestrator "${SMOKE_DB}"
fi

(
  cd backend
  env GOCACHE="${GOCACHE}" ORCH_ADDR="${API_ADDR}" ORCH_DATABASE_URL="${POSTGRES_URL}" ORCH_EMBEDDED_WORKERS=false ORCH_SCHEDULER_ENABLED=false go run ./cmd/api
) &
API_PID=$!

(
  cd backend
  env GOCACHE="${GOCACHE}" ORCH_DATABASE_URL="${POSTGRES_URL}" ORCH_WORKERS=1 ORCH_WORKER_ID=smoke-worker go run ./cmd/worker
) &
WORKER_PID=$!

for _ in {1..60}; do
  if curl -fsS "${API_URL}/readyz" >/dev/null; then
    break
  fi
  sleep 1
done
curl -fsS "${API_URL}/readyz" >/dev/null

job_response="$(curl -fsS -X POST "${API_URL}/v1/jobs" \
  -H 'Content-Type: application/json' \
  -d '{"name":"distributed-smoke","type":"video.transcode","max_attempts":2,"payload":{"duration_ms":100,"should_fail":false},"metadata":{"submitted_by":"smoke"}}')"

job_id="$(printf '%s' "${job_response}" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')"
if [[ -z "${job_id}" ]]; then
  echo "failed to parse job id from response: ${job_response}" >&2
  exit 1
fi

for _ in {1..60}; do
  job="$(curl -fsS "${API_URL}/v1/jobs/${job_id}")"
  if printf '%s' "${job}" | grep -q '"status":"succeeded"'; then
    curl -fsS "${API_URL}/v1/workers" | grep -q 'smoke-worker'
    curl -fsS "${API_URL}/metrics" | grep -q 'orchestrator_jobs_total'
    echo "distributed smoke test passed for job ${job_id}"
    exit 0
  fi
  if printf '%s' "${job}" | grep -Eq '"status":"(failed|dead_letter)"'; then
    echo "smoke job failed: ${job}" >&2
    exit 1
  fi
  sleep 1
done

echo "timed out waiting for smoke job ${job_id}" >&2
exit 1
