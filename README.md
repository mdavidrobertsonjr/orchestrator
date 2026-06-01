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

Run the production-style single-server website:

```bash
make website
```

Open:

```text
http://localhost:8080
```

## Postgres Mode

Start the API and dashboard with a local Postgres container:

```bash
make dev-postgres
```

Or run the single-server website with Postgres:

```bash
make website-postgres
```

Stop Postgres when you are done:

```bash
make postgres-stop
```

## English Job Requests

Set an OpenAI API key before starting the backend to enable the dashboard's English job request form:

```bash
OPENAI_API_KEY=sk-... make website
```

The backend uses `ORCH_OPENAI_MODEL=gpt-5.4-nano` by default and converts the request into the existing job fields before queueing it.

Email report requests are supported as simulated `report.email` jobs. The worker records recipients, subject, report kind, and schedule in the job logs, but it does not send real email until an email provider is configured in a later step.

## Checks

Run backend tests and build the frontend:

```bash
make test
```

Backend-specific API docs are in `backend/README.md`. Frontend-specific notes are in `frontend/README.md`.
