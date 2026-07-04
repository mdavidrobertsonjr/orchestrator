# Project Framing

Position the project as a distributed task orchestrator with natural-language job planning and production-style workflows:

> Built a Go-based distributed task orchestrator that converts English requests into structured jobs, runs them through a worker pool with heartbeats and retries, persists state in Postgres, exposes dashboard observability, and supports real workflows such as targeted new-grad SWE job monitoring.

The orchestrator should stay workflow-agnostic. New capabilities should be modeled as job types with explicit payload schemas, executors, logs, and result state. The system can support many recurring tasks over time, such as reports, monitors, notifications, data collection, and personal workflow automation.

The first major workflow is `jobs.monitor.new_grad`: a job type that checks company career pages and ATS providers for new-grad software engineering roles, persists discovered postings, deduplicates seen roles, ranks and filters matches, and emits immediate or daily notifications.

Longer term, the orchestrator should support multiple workflow families on the same platform:

- Recruiting workflows monitor ATS sources, dedupe postings, rank opportunities, and notify on high-signal matches.
- Finance workflows collect market data, calculate metrics, detect signals, and persist ranked results.
- Reporting workflows package stored results, failures, alerts, or metrics into scheduled summaries and digests.
- Alert workflows route important events to email, Slack, SMS, or other notification providers.

Target architecture:

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

Keep other use cases secondary. Generic demo jobs are useful for testing, and `report.email` supports notifications, but the job-monitoring workflow should be the main demo of the orchestrator's value.

Do not hard-code the product around one niche. The job-monitoring flow should be implemented as a reusable pattern: source adapters, normalized results, deduplication, ranking/filtering, notification, scheduling, and dashboard visibility. Future workflows should be able to reuse the same orchestration primitives.

Suggested platform steps:

1. Add a command preview and confirmation step before natural-language commands create jobs or workflows.
2. Add task-graph support for multi-step workflows such as fetch data -> analyze -> rank -> report -> notify.
3. Add a production notification provider such as Resend or SES alongside simulated and SMTP delivery.
4. Add finance workflows as a second demo family once task graphs are available.
5. Add authentication and per-user workspace isolation before exposing a hosted demo publicly.
