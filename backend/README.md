# Distributed Job Orchestrator

This repository is the backend control plane for a distributed job orchestration platform. The API accepts jobs and recurring workflows, persists state in memory or Postgres, queues work, and runs jobs asynchronously through embedded or standalone worker processes.

## Current Features

- Go HTTP API using the standard library.
- In-memory job store with state transitions and job logs.
- Optional Postgres-backed job store with automatic schema setup.
- Bounded in-memory queue.
- Embedded worker polling loop with configurable worker count.
- Email report job type with simulated delivery by default and SMTP delivery when configured.
- In-memory worker registry with heartbeat timestamps and current job tracking.
- Simulated executor for deterministic local development.
- Basic retries through `max_attempts`.
- Structured console logging with `slog`.
- Graceful shutdown on `SIGINT` and `SIGTERM`.

## Project Layout

```text
cmd/api/              API server entrypoint
internal/api/         HTTP routing and JSON handlers
internal/jobs/        Job model, queue interface, in-memory store
internal/worker/      Worker pool and executor
internal/workers/     Worker registry and liveness metadata
```

## Run

```bash
go run ./cmd/api
```

Optional configuration:

```bash
ORCH_ADDR=:8080 ORCH_WORKERS=4 ORCH_QUEUE_SIZE=256 go run ./cmd/api
```

The API serves the built dashboard from `../frontend/dist` by default. Override that path with `ORCH_STATIC_DIR`, or leave it empty to run only the API.

Enable English job requests with OpenAI:

```bash
OPENAI_API_KEY=sk-... ORCH_OPENAI_MODEL=gpt-5.4-nano go run ./cmd/api
```

If `OPENAI_API_KEY` is not set, structured job submission still works and `/v1/jobs/natural` returns `503`.

English email-report requests map to `report.email`. The worker records recipients, subject, kind, and schedule in logs and result state. Delivery is simulated by default and uses SMTP when configured.

Use Postgres for durable job state:

```bash
ORCH_DATABASE_URL='postgres://user:password@localhost:5432/orchestrator?sslmode=disable' \
  go run ./cmd/api
```

When `ORCH_DATABASE_URL` is set, the server stores jobs and logs in Postgres and runs the built-in schema migration on startup. Set `ORCH_AUTO_MIGRATE=false` to skip schema setup.

When `ORCH_DATABASE_URL` is set, the API defaults to a store-backed queue so separate processes can observe the same queued jobs. Set `ORCH_QUEUE_BACKEND=memory` to force the original in-memory queue.

Run a standalone worker against the same Postgres database:

```bash
ORCH_DATABASE_URL='postgres://user:password@localhost:5432/orchestrator?sslmode=disable' \
  go run ./cmd/worker
```

Set `ORCH_WORKER_ID` to choose the worker ID prefix shown in `/v1/workers`; worker processes append a numeric slot suffix such as `worker-hostname-1`.

Run the API as a control plane only, with no embedded workers:

```bash
ORCH_EMBEDDED_WORKERS=false \
ORCH_DATABASE_URL='postgres://user:password@localhost:5432/orchestrator?sslmode=disable' \
  go run ./cmd/api
```

The root Dockerfile has separate `api` and `worker` targets, and `docker compose --profile app up --build` runs the API, a standalone worker, and Postgres together.

## API

Health check:

```bash
curl http://localhost:8080/healthz
```

Readiness check:

```bash
curl http://localhost:8080/readyz
```

Submit a job:

```bash
curl -X POST http://localhost:8080/v1/jobs \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "example-transcode",
    "type": "video.transcode",
    "max_attempts": 2,
    "payload": {
      "duration_ms": 2000,
      "should_fail": false
    },
    "metadata": {
      "submitted_by": "local-dev"
    }
  }'
```

Submit an HTTP request job:

```bash
curl -X POST http://localhost:8080/v1/jobs \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "check-api",
    "type": "http.request",
    "max_attempts": 2,
    "payload": {
      "method": "GET",
      "url": "https://example.com/health",
      "timeout_ms": 5000,
      "duration_ms": 100
    }
  }'
```

List jobs:

```bash
curl http://localhost:8080/v1/jobs
```

Get one job:

```bash
curl http://localhost:8080/v1/jobs/<job-id>
```

Cancel a queued job:

```bash
curl -X POST http://localhost:8080/v1/jobs/<job-id>/cancel
```

Retry a failed, dead-lettered, or canceled job:

```bash
curl -X POST http://localhost:8080/v1/jobs/<job-id>/retry
```

Queue status:

```bash
curl http://localhost:8080/v1/queue
```

Runtime metrics:

```bash
curl http://localhost:8080/v1/metrics
```

Prometheus metrics:

```bash
curl http://localhost:8080/metrics
```

Worker status:

```bash
curl http://localhost:8080/v1/workers
```

Server-sent runtime snapshots:

```bash
curl http://localhost:8080/v1/events
```

## Stabilization

Run the normal verification suite:

```bash
make test
```

Run Postgres-backed integration checks from the repository root:

```bash
make smoke-postgres
```

For production releases, apply migrations explicitly and verify the services
can start without changing the schema automatically:

```bash
make migrate-postgres
make migration-gate
```

The integration suite covers startup migrations, SQL-backed pagination, notification delivery state, and scheduler advisory locking.

## Roadmap

- Real user/session management if the project moves beyond private deployment API keys.
- Provider-specific notification routing beyond tracked email delivery.
- Optional dedicated queue backend for higher-throughput deployments.
- Docker sandbox executor for trusted typed jobs that need process isolation.
