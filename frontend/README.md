# Orchestrator Frontend

React dashboard for the distributed job orchestrator control plane.

## Run

Start the backend first:

```bash
cd ../backend
go run ./cmd/api
```

Then start the frontend:

```bash
npm install
npm run dev
```

In development, Vite proxies `/healthz` and `/v1` requests to `http://localhost:8080`. Override the API base URL with:

```bash
VITE_API_BASE_URL=http://localhost:8080 npm run dev
```

## Current Views

- Summary metrics for queued, running, succeeded, failed, and active workers.
- Jobs table with status and attempts.
- Workers table with heartbeat and current job.
- Queue capacity panel.
- Submit job form for simulated workloads.
