# Job Types

This document summarizes the job executors and payload shapes supported by the orchestrator. `jobs.monitor.new_grad`, `report.email`, and `http.request` have typed behavior. Other job type strings can still run through the simulator path for demos and API testing.

## `jobs.monitor.new_grad`

Monitors public job-board feeds, normalizes postings, deduplicates already-seen roles, scores matches, stores structured results, and optionally sends alerts.

Example workflow payload:

```json
{
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
  "notifications": {
    "mode": "immediate",
    "recipients": ["you@example.com"],
    "quiet_hours_start": "22:00",
    "quiet_hours_end": "07:00",
    "timezone": "America/New_York",
    "max_alerts_per_workflow": 5
  }
}
```

Use `"mode": "digest_only"` to suppress immediate monitor alerts and rely on digest workflows instead. For a personal deployment, set `ORCH_DEFAULT_RECIPIENTS` once and omit recipients from individual monitor payloads. Workflow-specific recipients take precedence.

Supported monitor sources:

```json
{ "type": "greenhouse", "company": "ExampleCo", "board_token": "exampleco" }
```

```json
{ "type": "lever", "company": "ExampleCo", "account_name": "exampleco" }
```

```json
{ "type": "ashby", "company": "ExampleCo", "job_board_name": "exampleco" }
```

```json
{
  "type": "workday",
  "company": "ExampleCo",
  "tenant": "example",
  "site": "External",
  "careers_url": "https://example.wd5.myworkdayjobs.com"
}
```

```json
{ "type": "custom", "company": "ExampleCo", "url": "https://example.com/jobs.json" }
```

Per-source resiliency controls:

```json
{
  "rate_limit_ms": 500,
  "max_retries": 2,
  "backoff_ms": 250
}
```

The included seed script creates hourly monitors for Anduril, Palantir, Stripe, OpenAI, Anthropic, Databricks, Ramp, Figma, Plaid, Perplexity, Scale AI, Cursor, xAI, Vercel, Linear, Replit, Modal, and Baseten.

## `report.email`

Creates email report jobs. Delivery is simulated unless SMTP settings are configured.

Example digest workflow payload:

```json
{
  "report": {
    "recipients": ["you@example.com"],
    "subject": "Daily job monitor digest",
    "kind": "monitor_digest",
    "schedule": "daily"
  }
}
```

Configure SMTP:

```bash
ORCH_SMTP_HOST=smtp.example.com
ORCH_SMTP_PORT=587
ORCH_SMTP_USERNAME=apikey-or-user
ORCH_SMTP_PASSWORD=secret
ORCH_SMTP_FROM=orchestrator@example.com
ORCH_DEFAULT_RECIPIENTS=you@example.com
```

For Gmail, use an app password rather than your normal Google password.

## `http.request`

Runs a simple HTTP request and records the response summary.

```json
{
  "name": "check-api",
  "type": "http.request",
  "max_attempts": 2,
  "payload": {
    "method": "GET",
    "url": "https://example.com/health",
    "timeout_ms": 5000,
    "duration_ms": 100
  }
}
```

## Simulated Demo Jobs

Unknown or generic job types run through the simulator path: the executor logs receipt, waits for `duration_ms`, and fails when `should_fail` is true.

Natural-language planning currently allows these demo-oriented types:

- `video.transcode`
- `scrape.url`
- `python.script`
- `ai.inference`
- `data.pipeline`

Example:

```json
{
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
}
```

## Natural-Language Commands

When `OPENAI_API_KEY` is configured, the dashboard command box sends requests to `POST /v1/commands/natural`. The planner decides whether the request is a one-time job or a recurring workflow.

Examples:

- "Run a report now."
- "Monitor Datadog new-grad SWE roles in NYC every morning."

Recurring workflows can also be created through `POST /v1/workflows/natural`:

```bash
curl -X POST http://localhost:8080/v1/workflows/natural \
  -H 'Content-Type: application/json' \
  -d '{"prompt":"Monitor Datadog new-grad software engineering roles in NYC every day"}'
```

## Workflow Metadata And Audit

Workflow definitions can include ownership metadata. Structured workflow creation accepts either `metadata.owner` or a top-level `owner`; API clients can also send `X-Orchestrator-Owner`.

Mutating API actions record durable `audit.event` rows in the results store:

```bash
curl 'http://localhost:8080/v1/results?type=audit.event'
```
