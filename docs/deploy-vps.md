# Deploy On A Small VPS

This guide runs Orchestrator on one small Linux VPS with Docker Compose, Postgres, the API, a worker, and the built dashboard.

## Minimum Host

Recommended baseline:

- Ubuntu 24.04 LTS or Debian 12
- 2 vCPU
- 2 GB RAM
- 20 GB disk
- Docker Engine and Docker Compose plugin
- A DNS record such as `orchestrator.example.com`

Use Postgres mode for any real monitoring workload. In-memory mode is only for throwaway development.

## Environment

Create an `.env` file on the server:

```bash
ORCH_AUTH_TOKEN=replace-with-a-long-random-token
ORCH_DEFAULT_RECIPIENTS=you@example.com

ORCH_SMTP_HOST=smtp.gmail.com
ORCH_SMTP_PORT=587
ORCH_SMTP_USERNAME=you@example.com
ORCH_SMTP_PASSWORD=your-app-password
ORCH_SMTP_FROM=you@example.com

# Optional: enables natural-language commands in the dashboard.
OPENAI_API_KEY=
ORCH_OPENAI_MODEL=gpt-5.4-nano
```

Keep `ORCH_AUTH_TOKEN` long and random. The API uses it as the bearer token for dashboard API calls and operational endpoints.

For Gmail, enable 2-Step Verification on your Google account and create an app password. Put that 16-character app password in `ORCH_SMTP_PASSWORD`; do not use your normal Google password.

Validate the deployment settings before starting:

```bash
make check-deploy
```

## Start

From the repository root:

```bash
docker compose --profile app up --build -d
```

Check containers:

```bash
docker compose ps
```

Check readiness:

```bash
curl http://127.0.0.1:8080/readyz
```

Check authenticated API access:

```bash
curl -H "Authorization: Bearer ${ORCH_AUTH_TOKEN}" http://127.0.0.1:8080/v1/metrics
```

Seed monitors:

```bash
set -a
source .env
set +a
ORCH_MONITOR_URL=http://127.0.0.1:8080 make seed-job-monitors
```

The seed script uses `ORCH_AUTH_TOKEN` when it is set.

## Reverse Proxy And TLS

Put Caddy, nginx, or a cloud load balancer in front of the API. Terminate TLS at the proxy and forward to `127.0.0.1:8080`.

Example Caddyfile:

```text
orchestrator.example.com {
  reverse_proxy 127.0.0.1:8080
}
```

Expose only ports `80` and `443` publicly. Keep Postgres private to the Docker network.

## Upgrades

Pull or deploy the new code, then rebuild:

```bash
docker compose --profile app up --build -d
```

Workflows, postings, runs, results, notification deliveries, and audit events persist in Postgres. Startup migrations are recorded in `schema_migrations`. The scheduler resumes after restart and uses a Postgres advisory lock so only one API instance dispatches due workflows at a time.

Leave `ORCH_SCHEDULER_ENABLED` unset in normal deployments. Set it to `false` only for maintenance or smoke-test processes that should not dispatch due workflows.

## Backups

Back up Postgres regularly:

```bash
docker compose exec postgres pg_dump -U orchestrator orchestrator > orchestrator-$(date +%F).sql
```

Restore into a fresh database:

```bash
cat orchestrator-YYYY-MM-DD.sql | docker compose exec -T postgres psql -U orchestrator orchestrator
```

## Operations

Useful checks:

```bash
curl http://127.0.0.1:8080/healthz
curl http://127.0.0.1:8080/readyz
curl -H "Authorization: Bearer ${ORCH_AUTH_TOKEN}" http://127.0.0.1:8080/metrics
curl -H "Authorization: Bearer ${ORCH_AUTH_TOKEN}" 'http://127.0.0.1:8080/v1/results?type=audit.event&limit=20'
curl -H "Authorization: Bearer ${ORCH_AUTH_TOKEN}" 'http://127.0.0.1:8080/v1/results?type=monitor.source_health&limit=20'
```

Before deploying a new revision, run:

```bash
make test
make smoke-postgres
```

Watch logs:

```bash
docker compose logs -f api worker
```

Stop:

```bash
docker compose --profile app down
```

Use `down -v` only if you intentionally want to delete the database volume.
