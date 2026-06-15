# Distributed Job Orchestrator

This repository is the backend control-plane scaffold for a distributed job orchestration platform. The current MVP is intentionally small: an HTTP API accepts jobs, stores state in memory, queues work, and runs jobs asynchronously through an embedded worker pool.

The structure is designed so the in-memory queue and store can later be replaced with Redis and Postgres without changing the public API shape.

## Current Features

- Go HTTP API using the standard library.
- In-memory job store with state transitions and job logs.
- Optional Postgres-backed job store with automatic schema setup.
- Bounded in-memory queue.
- Embedded worker polling loop with configurable worker count.
- Simulated email report job type for reporting workflows.
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

English email-report requests map to `report.email`. The worker logs what would be sent, including recipients, subject, kind, and schedule, but does not deliver real email yet.

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

Run the API as a control plane only, with no embedded workers:

```bash
ORCH_EMBEDDED_WORKERS=false \
ORCH_DATABASE_URL='postgres://user:password@localhost:5432/orchestrator?sslmode=disable' \
  go run ./cmd/api
```

## API

Health check:

```bash
curl http://localhost:8080/healthz
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

List jobs:

```bash
curl http://localhost:8080/v1/jobs
```

Get one job:

```bash
curl http://localhost:8080/v1/jobs/<job-id>
```

Queue status:

```bash
curl http://localhost:8080/v1/queue
```

Runtime metrics:

```bash
curl http://localhost:8080/v1/metrics
```

Worker status:

```bash
curl http://localhost:8080/v1/workers
```

## Roadmap

- Redis-backed durable queue and pub/sub events.
- Separate worker binary for distributed execution.
- Worker registration, heartbeats, and liveness detection.
- Dead-letter queue and richer retry policies.
- WebSocket updates for dashboard state and logs.
- Docker sandbox executor.
- Prometheus-compatible metrics export.
