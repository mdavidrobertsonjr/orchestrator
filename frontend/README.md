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

- Overview metrics, operational alerts, queue health, and source-health failures.
- Jobs table with status, attempts, cancel/retry actions, and detail view.
- Discovered postings, structured results, scheduled workflows, and workflow run history.
- Workers table with heartbeat and current job.
- Command form for natural-language jobs/workflows plus structured job and workflow forms.
