# Distributed Job Orchestrator

This repository is the backend control-plane scaffold for a distributed job orchestration platform. The current MVP is intentionally small: an HTTP API accepts jobs, stores state in memory, queues work, and runs jobs asynchronously through an embedded worker pool.

The structure is designed so the in-memory queue and store can later be replaced with Redis and Postgres without changing the public API shape.

## Current Features

- Go HTTP API using the standard library.
- In-memory job store with state transitions and job logs.
- Bounded in-memory queue.
- Embedded worker polling loop with configurable worker count.
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

Worker status:

```bash
curl http://localhost:8080/v1/workers
```

## Roadmap

- Redis-backed durable queue and pub/sub events.
- Postgres-backed job metadata and execution history.
- Separate worker binary for distributed execution.
- Worker registration, heartbeats, and liveness detection.
- Dead-letter queue and richer retry policies.
- WebSocket updates for dashboard state and logs.
- Docker sandbox executor.
- Metrics endpoint and dashboard observability views.
