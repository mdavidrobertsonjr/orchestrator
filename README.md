# Distributed Job Orchestrator

Distributed job orchestration MVP with a Go control-plane API, embedded workers, optional Postgres job persistence, and a React operations dashboard.

The long-term direction is a general-purpose task orchestrator: a user should be able to describe useful work in English, have the system plan it into structured jobs, run those jobs through workers, persist state, retry failures, and report results. Job monitoring is the first flagship workflow because it is personally useful and gives the orchestrator a real recurring workload.

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

## Scheduled Workflows

Recurring workflows are stored separately from individual jobs. The scheduler polls enabled workflow definitions and creates a normal queued job whenever `next_run_at` is due. Each scheduled execution then uses the same worker pool, retries, logs, persistence, and dashboard views as manually submitted jobs.

Create a scheduled workflow:

```bash
curl -X POST http://localhost:8080/v1/workflows \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "datadog-new-grad-monitor",
    "job_type": "jobs.monitor.new_grad",
    "max_attempts": 2,
    "enabled": true,
    "interval_seconds": 86400,
    "payload": {
      "sources": [
        {
          "type": "greenhouse",
          "company": "Datadog",
          "board_token": "datadog"
        }
      ],
      "keywords": ["new grad", "university", "software engineer"],
      "excluded_keywords": ["senior", "staff", "principal"],
      "locations": ["new york", "nyc"],
      "min_score": 20,
      "notification_mode": "daily"
    },
    "metadata": {
      "submitted_by": "dashboard"
    }
  }'
```

List scheduled workflows:

```bash
curl http://localhost:8080/v1/workflows
```

The scheduler poll interval defaults to 30 seconds and can be configured with `ORCH_SCHEDULER_POLL_SECONDS`.

## Product Direction

Position the project as a distributed task orchestrator with natural-language job planning and production-style workflows:

> Built a Go-based distributed task orchestrator that converts English requests into structured jobs, runs them through a worker pool with heartbeats and retries, persists state in Postgres, exposes dashboard observability, and supports real workflows such as targeted new-grad SWE job monitoring.

The orchestrator should stay workflow-agnostic. New capabilities should be modeled as job types with explicit payload schemas, executors, logs, and result state. The system can support many recurring tasks over time, such as reports, monitors, notifications, data collection, and personal workflow automation.

The first major workflow should be `jobs.monitor.new_grad`: a job type that checks company career pages and ATS providers for new-grad software engineering roles, persists discovered postings, deduplicates seen roles, ranks/filter matches, and emits immediate or daily notifications.

Longer term, the orchestrator should support multiple workflow families on the same platform:

- Recruiting workflows monitor ATS sources, dedupe postings, rank opportunities, and notify on high-signal matches.
- Finance workflows collect market data, calculate metrics, detect signals, and persist ranked results.
- Reporting workflows package stored results, failures, alerts, or metrics into scheduled summaries and digests.
- Alert workflows route important events to email, Slack, SMS, or other notification providers.

The target architecture is:

```text
English request
  -> planner
  -> structured job or task graph
  -> queue
  -> workers
  -> persisted results
  -> notification or report
```

Example future requests:

- "Monitor every new-grad SWE opening at Datadog and notify me about NYC matches."
- "Every morning, find S&P 500 companies with unusual volume and send me a ranked summary."
- "Email me a daily digest of failed jobs, new postings, and important alerts."

Optimize for quality and speed over application volume. The system should find roles earlier than broad job boards, filter out low-signal postings, and surface a small set of opportunities worth acting on. The target profile is NYC or NYC-friendly new-grad SWE roles at companies that plausibly clear a strong existing baseline, including top tech companies, finance/fintech, AI/infrastructure/devtools/security startups, and selective remote roles.

Application support should stay human-in-the-loop. The public project story should be role discovery, ranking, tracking, and unresolved-question notifications, not bulk auto-apply. A later application workflow can create tasks such as `interested`, `needs_answer`, `ready_to_apply`, `applied`, `skipped`, and `interviewing`, while leaving final review and submission to the user.

Suggested implementation order for the job-monitoring workflow:

1. Add a posting model and store under `backend/internal/postings`.
2. Add a monitor job payload with companies, sources, keywords, excluded keywords, locations, and notification mode.
3. Implement source adapters for a fake/test source first, then Greenhouse and Lever-style public boards.
4. Normalize postings into one schema: company, title, URL, location, source, and posted/discovered timestamps.
5. Persist seen postings so repeat runs only alert on new matches.
6. Extend the worker executor with a real `jobs.monitor.new_grad` path.
7. Add ranking based on location fit, company quality, role fit, new-grad confidence, and whether the role is worth acting on quickly.
8. Show discovered postings, monitor runs, failures, rankings, and alert history in the dashboard.
9. Add scheduling and backoff so monitors can run daily or near real-time without manual submission.
10. Add an application task queue for tracking interest, unresolved free-response questions, submitted applications, and skipped roles.
11. Replace simulated email with a real notification provider when the workflow is stable.

Keep other use cases secondary. Generic demo jobs are useful for testing, and `report.email` supports notifications, but the job-monitoring workflow should be the main demo of the orchestrator's value.

Do not hard-code the product around one niche. The job-monitoring flow should be implemented as a reusable pattern: source adapters, normalized results, deduplication, ranking/filtering, notification, scheduling, and dashboard visibility. Future workflows should be able to reuse the same orchestration primitives.

Suggested platform steps after the first monitor workflow:

1. Add scheduling so any job type can run daily, hourly, or near real-time without manual submission.
2. Add durable workflow run records that separate a recurring workflow definition from each execution attempt.
3. Add a simple result store so workflow outputs can be queried by reports, alerts, and dashboards.
4. Extend natural-language planning to produce validated payloads for multiple job types, not only demo jobs.
5. Add task-graph support for multi-step workflows such as fetch data -> analyze -> rank -> report -> notify.
6. Add finance workflows as a second demo family once scheduling and result storage exist.

Example Greenhouse monitor payload:

```json
{
  "name": "datadog-new-grad-monitor",
  "type": "jobs.monitor.new_grad",
  "max_attempts": 2,
  "payload": {
    "sources": [
      {
        "type": "greenhouse",
        "company": "Datadog",
        "board_token": "datadog"
      }
    ],
    "keywords": ["new grad", "university", "software engineer"],
    "excluded_keywords": ["senior", "staff", "principal"],
    "locations": ["new york", "nyc"],
    "min_score": 20,
    "notification_mode": "daily"
  },
  "metadata": {
    "submitted_by": "dashboard"
  }
}
```

## Checks

Run backend tests and build the frontend:

```bash
make test
```

Backend-specific API docs are in `backend/README.md`. Frontend-specific notes are in `frontend/README.md`.
