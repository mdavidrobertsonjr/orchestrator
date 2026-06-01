# Distributed Job Orchestrator

Distributed job orchestration MVP with a Go control-plane API, embedded workers, optional Postgres job persistence, and a React operations dashboard.

## Quick Start

Install frontend dependencies once:

```bash
cd frontend
npm install
```

Run the API and dashboard together with in-memory storage:

```bash
make dev
```

Open the dashboard:

```text
http://localhost:5173
```

## Postgres Mode

Start the API and dashboard with a local Postgres container:

```bash
make dev-postgres
```

Stop Postgres when you are done:

```bash
make postgres-stop
```

## Checks

Run backend tests and build the frontend:

```bash
make test
```

Backend-specific API docs are in `backend/README.md`. Frontend-specific notes are in `frontend/README.md`.
