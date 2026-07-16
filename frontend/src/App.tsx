import { FormEvent, useCallback, useEffect, useMemo, useState } from "react";
import type { ReactNode } from "react";
import {
  Boxes,
  CheckCircle2,
  Clock3,
  Cpu,
  BellRing,
  ExternalLink,
  KeyRound,
  LayoutDashboard,
  ListChecks,
  Pause,
  Play,
  RefreshCw,
  Server,
  Send,
  Terminal,
  Trash2,
  ToggleRight,
  XCircle
} from "lucide-react";
import {
  cancelJob,
  clearAuthToken,
  createJob,
  createNaturalCommand,
  createWorkflow,
  deleteWorkflow,
  eventStreamURL,
  fetchHealth,
  fetchJobs,
  fetchMetrics,
  fetchPostings,
  fetchQueue,
  fetchResults,
  fetchWorkflowRuns,
  fetchWorkflows,
  fetchWorkers,
  getAuthToken,
  Job,
  JobStatus,
  OperationalAlert,
  Posting,
  PostingFilters,
  QueueStatus,
  Result,
  retryJob,
  RuntimeMetrics,
  runWorkflow,
  setAuthToken,
  updateWorkflow,
  Worker,
  WorkerStatus,
  Workflow,
  WorkflowRun
} from "./api";

const defaultQueue: QueueStatus = { queued: 0, capacity: 0 };

type DashboardSection = "overview" | "jobs" | "postings" | "workflows" | "results" | "workers" | "commands";

type NavItem = {
  id: DashboardSection;
  label: string;
  description: string;
  icon: ReactNode;
};

type JobTableRow = {
  job: Job;
  executionCount: number;
};

const navItems: NavItem[] = [
  {
    id: "overview",
    label: "Overview",
    description: "Runtime health",
    icon: <LayoutDashboard size={18} />
  },
  {
    id: "jobs",
    label: "Jobs",
    description: "Executions",
    icon: <ListChecks size={18} />
  },
  {
    id: "postings",
    label: "Postings",
    description: "Role matches",
    icon: <ExternalLink size={18} />
  },
  {
    id: "workflows",
    label: "Workflows",
    description: "Schedules",
    icon: <Clock3 size={18} />
  },
  {
    id: "results",
    label: "Results",
    description: "Outputs",
    icon: <Terminal size={18} />
  },
  {
    id: "workers",
    label: "Workers",
    description: "Executors",
    icon: <Server size={18} />
  },
  {
    id: "commands",
    label: "Commands",
    description: "Create work",
    icon: <Send size={18} />
  }
];

type SubmitState = {
  name: string;
  type: string;
  durationMs: number;
  maxAttempts: number;
  shouldFail: boolean;
};

const initialSubmitState: SubmitState = {
  name: "demo-transcode",
  type: "video.transcode",
  durationMs: 2000,
  maxAttempts: 2,
  shouldFail: false
};

type WorkflowFormState = {
  name: string;
  jobType: string;
  intervalSeconds: number;
  maxAttempts: number;
  enabled: boolean;
  payload: string;
};

type PostingFilterState = {
  q: string;
  company: string;
  source: string;
  location: string;
  minScore: number;
  pageSize: number;
};

const defaultPostingFilters: PostingFilterState = {
  q: "",
  company: "",
  source: "",
  location: "",
  minScore: 40,
  pageSize: 200
};

const priorityCompanies = [
  { label: "Palantir", value: "Palantir" },
  { label: "Anduril", value: "Anduril" },
  { label: "OpenAI", value: "OpenAI" },
  { label: "SpaceX / Starlink", value: "SpaceX, Starlink" },
  { label: "Notion", value: "Notion" },
  { label: "Robinhood", value: "Robinhood" }
];

const companyGroups = [
  {
    label: "Startups",
    value:
      "Cursor, Perplexity, Modal, Baseten, Confido, Zettabyte, Cockroach Labs, Astranis, Relativity Space, Kernel, Foxglove, Hipp Health, HIFI, Mirage, Meshy, Eventual"
  },
  {
    label: "Larger companies",
    value: "OpenAI, Anthropic, Palantir, SpaceX, Starlink, Databricks, Cloudflare, MongoDB, Stripe, Robinhood"
  },
  {
    label: "AI & infrastructure",
    value:
      "OpenAI, Anthropic, xAI, Databricks, Scale AI, Cursor, Perplexity, Modal, Baseten, Zettabyte, Cloudflare, MongoDB, Cockroach Labs, Kernel, Foxglove, Benchling, Hipp Health, Mirage, Meshy, Eventual, Cerebras"
  },
  {
    label: "Defense & space",
    value: "Anduril, SpaceX, Starlink, Astranis, Zipline, Relativity Space, Foxglove"
  },
  {
    label: "Developer tools",
    value: "Notion, Figma, Vercel, Linear, Replit"
  },
  { label: "Fintech", value: "Stripe, Ramp, Plaid, Robinhood" }
];

const earlyStageCompanies = new Set(["Confido", "Eventual", "Hipp Health", "Kernel", "Mirage", "Zettabyte"]);

const initialWorkflowForm: WorkflowFormState = {
  name: "datadog-new-grad-monitor",
  jobType: "jobs.monitor.new_grad",
  intervalSeconds: 86400,
  maxAttempts: 2,
  enabled: true,
  payload: JSON.stringify(
    {
      sources: [
        {
          type: "greenhouse",
          company: "Datadog",
          board_token: "datadog"
        }
      ],
      keywords: ["new grad", "university", "software engineer"],
      excluded_keywords: ["senior", "staff", "principal"],
      locations: ["new york", "nyc"],
      min_score: 40,
      notification_mode: "daily"
    },
    null,
    2
  )
};

export function App() {
  const [jobs, setJobs] = useState<Job[]>([]);
  const [postings, setPostings] = useState<Posting[]>([]);
  const [results, setResults] = useState<Result[]>([]);
  const [workflows, setWorkflows] = useState<Workflow[]>([]);
  const [workflowRuns, setWorkflowRuns] = useState<WorkflowRun[]>([]);
  const [workers, setWorkers] = useState<Worker[]>([]);
  const [queue, setQueue] = useState<QueueStatus>(defaultQueue);
  const [metrics, setMetrics] = useState<RuntimeMetrics | null>(null);
  const [form, setForm] = useState<SubmitState>(initialSubmitState);
  const [workflowForm, setWorkflowForm] = useState<WorkflowFormState>(initialWorkflowForm);
  const [postingFilters, setPostingFilters] = useState<PostingFilterState>(defaultPostingFilters);
  const [postingFilterForm, setPostingFilterForm] = useState<PostingFilterState>(defaultPostingFilters);
  const [commandPrompt, setCommandPrompt] = useState(
    "Monitor new-grad software engineering roles at OpenAI, Palantir, Anduril, and SpaceX every hour"
  );
  const [commandResult, setCommandResult] = useState<string | null>(null);
  const [selectedJobID, setSelectedJobID] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [apiOnline, setApiOnline] = useState(false);
  const [lastUpdated, setLastUpdated] = useState<Date | null>(null);
  const [activeSection, setActiveSection] = useState<DashboardSection>("overview");
  const [authRequired, setAuthRequired] = useState(false);
  const [authInput, setAuthInput] = useState(getAuthToken());

  const selectedJob = useMemo(
    () => jobs.find((job) => job.id === selectedJobID) ?? jobs[0],
    [jobs, selectedJobID]
  );

  const selectedJobResults = useMemo(
    () => (selectedJob ? results.filter((result) => result.job_id === selectedJob.id) : []),
    [results, selectedJob]
  );

  const jobRows = useMemo(() => groupJobsByWorkflow(jobs), [jobs]);

  const sourceHealthResults = useMemo(
    () => results.filter((result) => result.type === "monitor.source_health").slice(0, 6),
    [results]
  );

  const activeNavItem = navItems.find((item) => item.id === activeSection) ?? navItems[0];

  const summary = useMemo(() => {
    return {
      queued: jobs.filter((job) => job.status === "queued").length,
      running: jobs.filter((job) => job.status === "running").length,
      succeeded: jobs.filter((job) => job.status === "succeeded").length,
      failed: jobs.filter((job) => job.status === "failed").length,
      postings: metrics?.postings.total ?? postings.length,
      results: results.length,
      workflows: workflows.length,
      activeWorkers: workers.filter((worker) => worker.status !== "stopped").length
    };
  }, [jobs, metrics?.postings.total, postings, results, workflows, workers]);

  const postingQuery = useMemo<PostingFilters>(
    () => ({
      q: postingFilters.q,
      company: postingFilters.company,
      source: postingFilters.source,
      location: postingFilters.location,
      minScore: postingFilters.minScore,
      pageSize: postingFilters.pageSize
    }),
    [postingFilters]
  );

  const refresh = useCallback(async () => {
    try {
      const [, nextJobs, nextWorkers, nextQueue, nextMetrics, nextPostings, nextWorkflows, nextRuns, nextResults] =
        await Promise.all([
          fetchHealth(),
          fetchJobs(),
          fetchWorkers(),
          fetchQueue(),
          fetchMetrics(),
          fetchPostings(postingQuery),
          fetchWorkflows(),
          fetchWorkflowRuns(),
          fetchResults()
        ]);
      setJobs(nextJobs);
      setWorkers(nextWorkers);
      setQueue(nextQueue);
      setMetrics(nextMetrics);
      setPostings(sortPostings(nextPostings));
      setWorkflows(nextWorkflows);
      setWorkflowRuns(nextRuns);
      setResults(nextResults);
      setApiOnline(true);
      setLastUpdated(new Date());
      setError(null);
    } catch (err) {
      setApiOnline(false);
      const message = err instanceof Error ? err.message : "failed to refresh dashboard";
      if (message === "authentication required") {
        clearAuthToken();
        setAuthRequired(true);
      }
      setError(message);
    } finally {
      setLoading(false);
    }
  }, [postingQuery]);

  useEffect(() => {
    void refresh();
    if (typeof EventSource === "undefined") {
      const interval = setInterval(() => void refresh(), 2500);
      return () => clearInterval(interval);
    }

    let fallback: ReturnType<typeof setInterval> | undefined;
    const events = new EventSource(eventStreamURL());
    events.addEventListener("snapshot", () => void refresh());
    events.onerror = () => {
      events.close();
      fallback = setInterval(() => void refresh(), 2500);
    };

    return () => {
      events.close();
      if (fallback !== undefined) {
        clearInterval(fallback);
      }
    };
  }, [refresh]);

  function handlePostingFiltersSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setPostingFilters(postingFilterForm);
  }

  function handlePostingFiltersReset() {
    setPostingFilterForm(defaultPostingFilters);
    setPostingFilters(defaultPostingFilters);
  }

  function handleAuthSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setAuthToken(authInput);
    setAuthRequired(false);
    setError(null);
    void refresh();
  }

  function handleSignOut() {
    clearAuthToken();
    setAuthInput("");
    setAuthRequired(true);
    setApiOnline(false);
    setError(null);
  }

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setSubmitting(true);
    setError(null);

    try {
      const created = await createJob({
        name: form.name.trim() || form.type.trim(),
        type: form.type.trim(),
        max_attempts: form.maxAttempts,
        payload: {
          duration_ms: form.durationMs,
          should_fail: form.shouldFail
        },
        metadata: {
          submitted_by: "dashboard"
        }
      });
      setSelectedJobID(created.id);
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : "failed to submit job");
    } finally {
      setSubmitting(false);
    }
  }

  async function handleNaturalCommandSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setSubmitting(true);
    setError(null);
    setCommandResult(null);

    try {
      const created = await createNaturalCommand({ prompt: commandPrompt.trim() });
      if (created.action === "job" && created.job) {
        setSelectedJobID(created.job.id);
        setActiveSection("jobs");
        setCommandResult(`Queued job: ${created.job.name}`);
      } else if (created.action === "workflow" && created.workflow) {
        setActiveSection("workflows");
        setCommandResult(`Created workflow: ${created.workflow.name}`);
      }
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : "failed to run command");
    } finally {
      setSubmitting(false);
    }
  }

  async function handleWorkflowSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setSubmitting(true);
    setError(null);

    try {
      const payload = JSON.parse(workflowForm.payload) as Record<string, unknown>;
      await createWorkflow({
        name: workflowForm.name.trim() || workflowForm.jobType.trim(),
        job_type: workflowForm.jobType.trim(),
        max_attempts: workflowForm.maxAttempts,
        enabled: workflowForm.enabled,
        interval_seconds: workflowForm.intervalSeconds,
        payload,
        metadata: {
          submitted_by: "dashboard"
        }
      });
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : "failed to create workflow");
    } finally {
      setSubmitting(false);
    }
  }

  async function handleWorkflowToggle(workflow: Workflow) {
    setError(null);

    try {
      await updateWorkflow(workflow.id, { enabled: !workflow.enabled });
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : "failed to update workflow");
    }
  }

  async function handleWorkflowRun(workflow: Workflow) {
    setError(null);

    try {
      const job = await runWorkflow(workflow.id);
      setSelectedJobID(job.id);
      setActiveSection("jobs");
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : "failed to run workflow");
    }
  }

  async function handleWorkflowDelete(workflow: Workflow) {
	if (!window.confirm(`Delete disabled workflow "${workflow.name}"? Run history and jobs will be retained.`)) {
	  return;
	}
	setError(null);
	try {
	  await deleteWorkflow(workflow.id);
	  await refresh();
	} catch (err) {
	  setError(err instanceof Error ? err.message : "failed to delete workflow");
	}
  }

  async function handleCancelJob(job: Job) {
    setError(null);

    try {
      await cancelJob(job.id);
      if (selectedJobID === job.id) {
        setSelectedJobID(job.id);
      }
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : "failed to cancel job");
    }
  }

  async function handleRetryJob(job: Job) {
    setError(null);

    try {
      await retryJob(job.id);
      setSelectedJobID(job.id);
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : "failed to retry job");
    }
  }

  const queueFill = queue.capacity ? (queue.queued / queue.capacity) * 100 : 0;

  if (authRequired && getAuthToken() === "") {
    return <AuthScreen value={authInput} onChange={setAuthInput} onSubmit={handleAuthSubmit} />;
  }

  const overviewPanels = (
    <>
      <SummaryGrid summary={summary} />
      <section className="dashboard-grid compact-grid">
        <Panel title="Operational Alerts" subtitle={`${metrics?.alerts?.length ?? 0} active signals`}>
          <OperationalAlertsPanel alerts={metrics?.alerts ?? []} loading={loading} />
        </Panel>
        <Panel title="Source Health" subtitle={`${sourceHealthResults.length} recent source events`}>
          <SourceHealthPanel results={sourceHealthResults} loading={loading} onSelectJob={setSelectedJobID} />
        </Panel>
      </section>
      <section className="dashboard-grid">
        <Panel title="Recent Jobs" subtitle={jobRowsSubtitle(jobRows.length, jobs.length)}>
          <JobsTable
            rows={jobRows.slice(0, 8)}
            selectedJobID={selectedJob?.id}
            onSelect={setSelectedJobID}
            onCancel={(job) => void handleCancelJob(job)}
            onRetry={(job) => void handleRetryJob(job)}
            loading={loading}
          />
        </Panel>
        <Panel title="Queue" subtitle="In-memory capacity">
          <QueueMeter queue={queue} fill={queueFill} />
        </Panel>
        <Panel
          title="Runtime Metrics"
          subtitle={metrics ? `Generated ${relativeTime(metrics.generated_at)}` : "Waiting for metrics"}
        >
          <RuntimeMetricsPanel metrics={metrics} loading={loading} />
        </Panel>
        <Panel title="Recent Results" subtitle={`${results.length} structured outputs`}>
          <ResultsTable results={results} loading={loading} onSelectJob={setSelectedJobID} />
        </Panel>
        <Panel title="Job Detail" subtitle={selectedJob ? selectedJob.id.slice(0, 12) : "No job selected"}>
          {selectedJob ? <JobDetail job={selectedJob} results={selectedJobResults} /> : <EmptyState label="No jobs yet" />}
        </Panel>
      </section>
    </>
  );

  const sectionContent: Record<DashboardSection, ReactNode> = {
    overview: overviewPanels,
    jobs: (
      <section className="split-view">
        <Panel title="Jobs" subtitle={jobRowsSubtitle(jobRows.length, jobs.length)}>
          <JobsTable
            rows={jobRows}
            selectedJobID={selectedJob?.id}
            onSelect={setSelectedJobID}
            onCancel={(job) => void handleCancelJob(job)}
            onRetry={(job) => void handleRetryJob(job)}
            loading={loading}
          />
        </Panel>
        <Panel title="Job Detail" subtitle={selectedJob ? selectedJob.id.slice(0, 12) : "No job selected"}>
          {selectedJob ? <JobDetail job={selectedJob} results={selectedJobResults} /> : <EmptyState label="No jobs yet" />}
        </Panel>
      </section>
    ),
    postings: (
      <Panel
        title="Discovered Postings"
        subtitle={`${postings.length} shown of ${metrics?.postings.total ?? postings.length} total`}
      >
        <PostingFilterBar
          filters={postingFilterForm}
          onChange={setPostingFilterForm}
          onReset={handlePostingFiltersReset}
          onSubmit={handlePostingFiltersSubmit}
        />
        <PostingsTable postings={postings} loading={loading} />
      </Panel>
    ),
    workflows: (
      <Panel title="Scheduled Workflows" subtitle={`${workflows.length} recurring definitions`}>
        <WorkflowsTable
          workflows={workflows}
          runs={workflowRuns}
          jobs={jobs}
          loading={loading}
          onToggle={(workflow) => void handleWorkflowToggle(workflow)}
          onRun={(workflow) => void handleWorkflowRun(workflow)}
          onDelete={(workflow) => void handleWorkflowDelete(workflow)}
          onSelectJob={setSelectedJobID}
        />
      </Panel>
    ),
    results: (
      <section className="split-view">
        <Panel title="Recent Results" subtitle={`${results.length} structured outputs`}>
          <ResultsTable results={results} loading={loading} onSelectJob={setSelectedJobID} />
        </Panel>
        <Panel title="Source Health" subtitle={`${sourceHealthResults.length} recent source events`}>
          <SourceHealthPanel results={sourceHealthResults} loading={loading} onSelectJob={setSelectedJobID} />
        </Panel>
      </section>
    ),
    workers: (
      <section className="split-view">
        <Panel title="Workers" subtitle={`${workers.length} registered nodes`}>
          <WorkersTable workers={workers} loading={loading} />
        </Panel>
        <Panel title="Queue" subtitle="In-memory capacity">
          <QueueMeter queue={queue} fill={queueFill} />
        </Panel>
        <Panel
          title="Runtime Metrics"
          subtitle={metrics ? `Generated ${relativeTime(metrics.generated_at)}` : "Waiting for metrics"}
        >
          <RuntimeMetricsPanel metrics={metrics} loading={loading} />
        </Panel>
        <Panel title="Operational Alerts" subtitle={`${metrics?.alerts?.length ?? 0} active signals`}>
          <OperationalAlertsPanel alerts={metrics?.alerts ?? []} loading={loading} />
        </Panel>
      </section>
    ),
    commands: (
      <section className="command-grid">
        <Panel title="Tell Orchestrator What To Do" subtitle="Turn plain English into a job or recurring workflow">
          <NaturalCommandForm
            prompt={commandPrompt}
            result={commandResult}
            submitting={submitting}
            onChange={setCommandPrompt}
            onSubmit={handleNaturalCommandSubmit}
          />
        </Panel>
        <Panel title="Schedule Workflow" subtitle="Create recurring work">
          <WorkflowForm
            form={workflowForm}
            submitting={submitting}
            onChange={setWorkflowForm}
            onSubmit={handleWorkflowSubmit}
          />
        </Panel>
        <Panel title="Submit Job" subtitle="Create a simulated workload">
          <SubmitJobForm form={form} submitting={submitting} onChange={setForm} onSubmit={handleSubmit} />
        </Panel>
      </section>
    )
  };

  return (
    <div className="app-shell">
      <aside className="sidebar">
        <div className="brand">
          <div className="brand-mark">
            <Boxes size={22} />
          </div>
          <div>
            <strong>Orchestrator</strong>
            <span>Control Plane</span>
          </div>
        </div>

        <nav className="nav-list" aria-label="Main navigation">
          {navItems.map((item) => (
            <button
              className={`nav-item ${item.id === activeSection ? "active" : ""}`}
              type="button"
              key={item.id}
              onClick={() => setActiveSection(item.id)}
            >
              {item.icon}
              <span>
                <strong>{item.label}</strong>
                <small>{item.description}</small>
              </span>
            </button>
          ))}
        </nav>
      </aside>

      <main className="main">
        <header className="topbar">
          <div>
            <p className="eyebrow">Distributed Job Runtime</p>
            <h1>{activeNavItem.label}</h1>
          </div>
          <div className="topbar-actions">
            <span className={`connection-status ${apiOnline ? "online" : "offline"}`}>
              {apiOnline ? "API online" : "API offline"}
            </span>
            <span className="updated">{lastUpdated ? `Updated ${formatTime(lastUpdated)}` : "Not synced"}</span>
            {getAuthToken() && (
              <button className="icon-button" type="button" onClick={handleSignOut} aria-label="Clear auth token" title="Clear auth token">
                <KeyRound size={18} />
              </button>
            )}
            <button className="icon-button" type="button" onClick={() => void refresh()} aria-label="Refresh">
              <RefreshCw size={18} />
            </button>
          </div>
        </header>

        {error && <div className="alert">{error}</div>}

        {sectionContent[activeSection]}
      </main>
    </div>
  );
}

function AuthScreen({
  value,
  onChange,
  onSubmit
}: {
  value: string;
  onChange: (value: string) => void;
  onSubmit: (event: FormEvent<HTMLFormElement>) => void;
}) {
  return (
    <main className="auth-shell">
      <form className="auth-panel" onSubmit={onSubmit}>
        <div className="brand-mark">
          <KeyRound size={22} />
        </div>
        <h1>Orchestrator</h1>
        <label>
          <span>Access token</span>
          <input
            value={value}
            onChange={(event) => onChange(event.target.value)}
            autoComplete="current-password"
            autoFocus
            type="password"
          />
        </label>
        <button className="primary-button" type="submit" disabled={value.trim() === ""}>
          Unlock
        </button>
      </form>
    </main>
  );
}

function SummaryGrid({
  summary
}: {
  summary: {
    queued: number;
    running: number;
    succeeded: number;
    failed: number;
    postings: number;
    workflows: number;
    results: number;
    activeWorkers: number;
  };
}) {
  return (
    <section className="summary-grid" aria-label="Summary">
      <SummaryCard label="Queued" value={summary.queued} icon={<Clock3 size={20} />} tone="queued" />
      <SummaryCard label="Running" value={summary.running} icon={<Play size={20} />} tone="running" />
      <SummaryCard label="Succeeded" value={summary.succeeded} icon={<CheckCircle2 size={20} />} tone="succeeded" />
      <SummaryCard label="Failed" value={summary.failed} icon={<XCircle size={20} />} tone="failed" />
      <SummaryCard label="Postings" value={summary.postings} icon={<ExternalLink size={20} />} tone="postings" />
      <SummaryCard label="Workflows" value={summary.workflows} icon={<Clock3 size={20} />} tone="workflows" />
      <SummaryCard label="Results" value={summary.results} icon={<Terminal size={20} />} tone="results" />
      <SummaryCard label="Workers" value={summary.activeWorkers} icon={<Cpu size={20} />} tone="workers" />
    </section>
  );
}

function Panel({ title, subtitle, children }: { title: string; subtitle: string; children: ReactNode }) {
  return (
    <section className="panel">
      <div className="panel-header">
        <div>
          <h2>{title}</h2>
          <p>{subtitle}</p>
        </div>
      </div>
      {children}
    </section>
  );
}

function QueueMeter({ queue, fill }: { queue: QueueStatus; fill: number }) {
  return (
    <>
      <div className="queue-meter">
        <div>
          <strong>{queue.queued}</strong>
          <span>queued</span>
        </div>
        <div>
          <strong>{queue.capacity}</strong>
          <span>capacity</span>
        </div>
      </div>
      <div className="meter-track">
        <div className="meter-fill" style={{ width: `${fill}%` }} />
      </div>
    </>
  );
}

function RuntimeMetricsPanel({ metrics, loading }: { metrics: RuntimeMetrics | null; loading: boolean }) {
  if (loading) {
    return <EmptyState label="Loading metrics" />;
  }
  if (!metrics) {
    return <EmptyState label="Metrics unavailable" />;
  }

  const queuePercent = Math.round(metrics.queue.utilization * 100);

  return (
    <div className="metrics-panel">
      <div className="metric-row">
        <span>Queue utilization</span>
        <strong>{queuePercent}%</strong>
      </div>
      <div className="metric-row">
        <span>Active workers</span>
        <strong>
          {metrics.workers.active}/{metrics.workers.total}
        </strong>
      </div>
      <div className="metric-row">
        <span>Retry attempts</span>
        <strong>{metrics.jobs.retry_attempts}</strong>
      </div>
      <div className="metric-row">
        <span>Leased jobs</span>
        <strong>{metrics.jobs.leased}</strong>
      </div>
      <div className={`metric-row ${metrics.jobs.expired_leases > 0 ? "warning" : ""}`}>
        <span>Expired leases</span>
        <strong>{metrics.jobs.expired_leases}</strong>
      </div>
      <div className={`metric-row ${metrics.workflows.due > 0 ? "attention" : ""}`}>
        <span>Due workflows</span>
        <strong>{metrics.workflows.due}</strong>
      </div>
      <div className="metric-row">
        <span>Run records</span>
        <strong>{metrics.workflows.run_records}</strong>
      </div>
      <div className="metric-row">
        <span>Total attempts</span>
        <strong>{metrics.jobs.attempts}</strong>
      </div>
      <div className={`metric-row ${(metrics.notifications?.failed ?? 0) > 0 ? "warning" : ""}`}>
        <span>Notification failures</span>
        <strong>{metrics.notifications?.failed ?? 0}</strong>
      </div>
    </div>
  );
}

function OperationalAlertsPanel({ alerts, loading }: { alerts: OperationalAlert[]; loading: boolean }) {
  if (loading) {
    return <EmptyState label="Loading alerts" />;
  }
  if (alerts.length === 0) {
    return (
      <div className="signal-list">
        <div className="signal-row ok">
          <CheckCircle2 size={17} />
          <div>
            <strong>No active alerts</strong>
            <span>Runtime metrics are inside configured thresholds</span>
          </div>
        </div>
      </div>
    );
  }

  return (
    <div className="signal-list">
      {alerts.map((alert) => (
        <div className={`signal-row ${alert.severity}`} key={`${alert.name}-${alert.severity}`}>
          <BellRing size={17} />
          <div>
            <strong>{formatAlertName(alert.name)}</strong>
            <span>{alert.message}</span>
          </div>
          <b>{alert.value}</b>
        </div>
      ))}
    </div>
  );
}

function SourceHealthPanel({
  results,
  loading,
  onSelectJob
}: {
  results: Result[];
  loading: boolean;
  onSelectJob: (id: string) => void;
}) {
  if (loading) {
    return <EmptyState label="Loading source health" />;
  }
  if (results.length === 0) {
    return <EmptyState label="No source failures recorded" />;
  }

  return (
    <div className="signal-list">
      {results.map((result) => (
        <div className="source-health-row" key={result.id}>
          <div>
            <strong>{String(result.data?.status ?? "source event")}</strong>
            <span>{result.summary || result.id.slice(0, 12)}</span>
            {Array.isArray(result.data?.sources) && result.data.sources.length > 0 && (
              <small>{result.data.sources.map(String).join(", ")}</small>
            )}
          </div>
          <div className="source-health-actions">
            <span>{relativeTime(result.created_at)}</span>
            {result.job_id && (
              <button className="link-button" type="button" onClick={() => onSelectJob(result.job_id as string)}>
                Job
              </button>
            )}
          </div>
        </div>
      ))}
    </div>
  );
}

function NaturalCommandForm({
  prompt,
  result,
  submitting,
  onChange,
  onSubmit
}: {
  prompt: string;
  result: string | null;
  submitting: boolean;
  onChange: (value: string) => void;
  onSubmit: (event: FormEvent<HTMLFormElement>) => void;
}) {
  return (
    <form className="submit-form" onSubmit={onSubmit}>
      <label>
        <span>Request</span>
        <textarea value={prompt} onChange={(event) => onChange(event.target.value)} rows={4} />
      </label>
      <button className="primary-button" type="submit" disabled={submitting || prompt.trim() === ""}>
        <Send size={16} />
        {submitting ? "Planning" : "Run Command"}
      </button>
      {result && <div className="success-note">{result}</div>}
    </form>
  );
}

function WorkflowForm({
  form,
  submitting,
  onChange,
  onSubmit
}: {
  form: WorkflowFormState;
  submitting: boolean;
  onChange: (value: WorkflowFormState) => void;
  onSubmit: (event: FormEvent<HTMLFormElement>) => void;
}) {
  return (
    <form className="submit-form" onSubmit={onSubmit}>
      <label>
        <span>Name</span>
        <input
          value={form.name}
          onChange={(event) => onChange({ ...form, name: event.target.value })}
          placeholder="datadog-new-grad-monitor"
        />
      </label>
      <label>
        <span>Job Type</span>
        <select value={form.jobType} onChange={(event) => onChange({ ...form, jobType: event.target.value })}>
          <option value="jobs.monitor.new_grad">jobs.monitor.new_grad</option>
          <option value="report.email">report.email</option>
          <option value="video.transcode">video.transcode</option>
          <option value="http.request">http.request</option>
          <option value="scrape.url">scrape.url</option>
          <option value="data.pipeline">data.pipeline</option>
        </select>
      </label>
      <div className="field-row">
        <label>
          <span>Interval</span>
          <select
            value={form.intervalSeconds}
            onChange={(event) => onChange({ ...form, intervalSeconds: Number(event.target.value) })}
          >
            <option value={900}>15 minutes</option>
            <option value={3600}>Hourly</option>
            <option value={86400}>Daily</option>
          </select>
        </label>
        <label>
          <span>Attempts</span>
          <input
            type="number"
            min={1}
            max={10}
            value={form.maxAttempts}
            onChange={(event) => onChange({ ...form, maxAttempts: Number(event.target.value) })}
          />
        </label>
      </div>
      <label>
        <span>Payload JSON</span>
        <textarea
          className="code-textarea"
          value={form.payload}
          rows={10}
          spellCheck={false}
          onChange={(event) => onChange({ ...form, payload: event.target.value })}
        />
      </label>
      <label className="checkbox-field">
        <input
          type="checkbox"
          checked={form.enabled}
          onChange={(event) => onChange({ ...form, enabled: event.target.checked })}
        />
        <span>Enabled</span>
      </label>
      <button className="primary-button" type="submit" disabled={submitting || form.jobType.trim() === ""}>
        <Send size={16} />
        {submitting ? "Scheduling" : "Schedule Workflow"}
      </button>
    </form>
  );
}

function SubmitJobForm({
  form,
  submitting,
  onChange,
  onSubmit
}: {
  form: SubmitState;
  submitting: boolean;
  onChange: (value: SubmitState) => void;
  onSubmit: (event: FormEvent<HTMLFormElement>) => void;
}) {
  return (
    <form className="submit-form" onSubmit={onSubmit}>
      <label>
        <span>Name</span>
        <input
          value={form.name}
          onChange={(event) => onChange({ ...form, name: event.target.value })}
          placeholder="demo-transcode"
        />
      </label>
      <label>
        <span>Type</span>
        <select value={form.type} onChange={(event) => onChange({ ...form, type: event.target.value })}>
          <option value="video.transcode">video.transcode</option>
          <option value="scrape.url">scrape.url</option>
          <option value="python.script">python.script</option>
          <option value="ai.inference">ai.inference</option>
          <option value="data.pipeline">data.pipeline</option>
          <option value="report.email">report.email</option>
        </select>
      </label>
      <div className="field-row">
        <label>
          <span>Duration</span>
          <input
            type="number"
            min={100}
            max={30000}
            step={100}
            value={form.durationMs}
            onChange={(event) => onChange({ ...form, durationMs: Number(event.target.value) })}
          />
        </label>
        <label>
          <span>Attempts</span>
          <input
            type="number"
            min={1}
            max={10}
            value={form.maxAttempts}
            onChange={(event) => onChange({ ...form, maxAttempts: Number(event.target.value) })}
          />
        </label>
      </div>
      <label className="checkbox-field">
        <input
          type="checkbox"
          checked={form.shouldFail}
          onChange={(event) => onChange({ ...form, shouldFail: event.target.checked })}
        />
        <span>Simulate failure</span>
      </label>
      <button className="primary-button" type="submit" disabled={submitting || form.type.trim() === ""}>
        <Send size={16} />
        {submitting ? "Submitting" : "Submit Job"}
      </button>
    </form>
  );
}

function SummaryCard({
  label,
  value,
  icon,
  tone
}: {
  label: string;
  value: number;
  icon: ReactNode;
  tone: string;
}) {
  return (
    <article className={`summary-card ${tone}`}>
      <div className="summary-icon">{icon}</div>
      <div>
        <span>{label}</span>
        <strong>{value}</strong>
      </div>
    </article>
  );
}

function JobsTable({
  rows,
  selectedJobID,
  onSelect,
  onCancel,
  onRetry,
  loading
}: {
  rows: JobTableRow[];
  selectedJobID?: string;
  onSelect: (id: string) => void;
  onCancel: (job: Job) => void;
  onRetry: (job: Job) => void;
  loading: boolean;
}) {
  if (loading) {
    return <EmptyState label="Loading jobs" />;
  }
  if (rows.length === 0) {
    return <EmptyState label="No jobs submitted" />;
  }

  return (
    <div className="table-wrap">
      <table>
        <thead>
          <tr>
            <th>Name</th>
            <th>Type</th>
            <th>Status</th>
            <th>Attempts</th>
            <th>Updated</th>
            <th>Action</th>
          </tr>
        </thead>
        <tbody>
          {rows.map(({ job, executionCount }) => (
            <tr
              className={job.id === selectedJobID ? "selected" : ""}
              key={job.id}
              onClick={() => onSelect(job.id)}
            >
              <td>
                <strong>{job.name}</strong>
                <span>
                  {job.id.slice(0, 12)}
                  {executionCount > 1 ? ` · ${executionCount} executions` : ""}
                </span>
              </td>
              <td>{job.type}</td>
              <td>
                <StatusPill status={job.status} />
              </td>
              <td>
                {job.attempts}/{job.max_attempts}
              </td>
              <td>{relativeTime(job.updated_at)}</td>
              <td>
                {job.status === "queued" || isRetryableJob(job) ? (
                  <div className="table-actions no-wrap">
                    {job.status === "queued" && (
                      <button
                        className="table-action danger"
                        type="button"
                        onClick={(event) => {
                          event.stopPropagation();
                          onCancel(job);
                        }}
                        aria-label="Cancel queued job"
                        title="Cancel queued job"
                      >
                        <XCircle size={16} />
                        Cancel
                      </button>
                    )}
                    {isRetryableJob(job) && (
                      <button
                        className="table-action"
                        type="button"
                        onClick={(event) => {
                          event.stopPropagation();
                          onRetry(job);
                        }}
                        aria-label="Retry job"
                        title="Retry job"
                      >
                        <RefreshCw size={16} />
                        Retry
                      </button>
                    )}
                  </div>
                ) : (
                  "-"
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function isRetryableJob(job: Job) {
  return job.status === "failed" || job.status === "dead_letter" || job.status === "canceled";
}

function WorkersTable({ workers, loading }: { workers: Worker[]; loading: boolean }) {
  if (loading) {
    return <EmptyState label="Loading workers" />;
  }
  if (workers.length === 0) {
    return <EmptyState label="No workers registered" />;
  }

  return (
    <div className="table-wrap">
      <table>
        <thead>
          <tr>
            <th>ID</th>
            <th>Status</th>
            <th>Current Job</th>
            <th>Last Heartbeat</th>
          </tr>
        </thead>
        <tbody>
          {workers.map((worker) => (
            <tr key={worker.id}>
              <td>
                <strong>{worker.id}</strong>
              </td>
              <td>
                <WorkerPill status={worker.status} />
              </td>
              <td>{worker.current_job_id ? worker.current_job_id.slice(0, 12) : "-"}</td>
              <td>{relativeTime(worker.last_heartbeat)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function PostingFilterBar({
  filters,
  onChange,
  onReset,
  onSubmit
}: {
  filters: PostingFilterState;
  onChange: (filters: PostingFilterState) => void;
  onReset: () => void;
  onSubmit: (event: FormEvent<HTMLFormElement>) => void;
}) {
  return (
    <form className="posting-filters" onSubmit={onSubmit}>
      <label>
        <span>Search</span>
        <input
          value={filters.q}
          onChange={(event) => onChange({ ...filters, q: event.target.value })}
          placeholder="Show me OpenAI new grad software jobs"
        />
      </label>
      <label>
        <span>Company</span>
        <input
          value={filters.company}
          onChange={(event) => onChange({ ...filters, company: event.target.value })}
          placeholder="Palantir, Stripe"
        />
      </label>
      <label>
        <span>Location</span>
        <input
          value={filters.location}
          onChange={(event) => onChange({ ...filters, location: event.target.value })}
          placeholder="New York, Los Angeles"
        />
      </label>
      <label>
        <span>Source</span>
        <select value={filters.source} onChange={(event) => onChange({ ...filters, source: event.target.value })}>
          <option value="">Any</option>
          <option value="ashby">Ashby</option>
          <option value="greenhouse">Greenhouse</option>
          <option value="lever">Lever</option>
          <option value="workday">Workday</option>
          <option value="fake">Fake</option>
        </select>
      </label>
      <label>
        <span>Min score</span>
        <select
          value={filters.minScore}
          onChange={(event) => onChange({ ...filters, minScore: Number(event.target.value) })}
        >
          <option value={60}>60+</option>
          <option value={80}>80+</option>
          <option value={40}>40+</option>
          <option value={20}>20+</option>
          <option value={0}>All</option>
        </select>
      </label>
      <label>
        <span>Limit</span>
        <select
          value={filters.pageSize}
          onChange={(event) => onChange({ ...filters, pageSize: Number(event.target.value) })}
        >
          <option value={100}>100</option>
          <option value={200}>200</option>
          <option value={500}>500</option>
        </select>
      </label>
      <div className="posting-filter-actions">
        <button className="table-action" type="button" onClick={onReset}>
          Reset
        </button>
        <button className="primary-button" type="submit">
          Apply
        </button>
      </div>
      <div className="priority-company-filter" aria-label="Priority company filters">
        <span>Priority companies</span>
        <div>
          {priorityCompanies.map((company) => {
            const values = company.value.split(",").map((value) => value.trim().toLowerCase());
            const active = values.every((value) => companyFilterValues(filters.company).includes(value));
            return (
              <button
                className={active ? "active" : ""}
                key={company.label}
                type="button"
                aria-pressed={active}
                onClick={() => onChange({ ...filters, company: toggleCompanyFilter(filters.company, company.value) })}
              >
                {company.label}
              </button>
            );
          })}
        </div>
      </div>
      <div className="priority-company-filter" aria-label="Company group filters">
        <span>Company groups</span>
        <div>
          {companyGroups.map((group) => {
            const values = group.value.split(",").map((value) => value.trim().toLowerCase());
            const active = values.every((value) => companyFilterValues(filters.company).includes(value));
            return (
              <button
                className={active ? "active" : ""}
                key={group.label}
                type="button"
                aria-pressed={active}
                onClick={() => onChange({ ...filters, company: toggleCompanyFilter(filters.company, group.value) })}
              >
                {group.label}
              </button>
            );
          })}
        </div>
      </div>
    </form>
  );
}

function companyFilterValues(value: string) {
  return value
    .split(",")
    .map((company) => company.trim().toLowerCase())
    .filter(Boolean);
}

function toggleCompanyFilter(value: string, company: string) {
  const companies = value
    .split(",")
    .map((item) => item.trim())
    .filter(Boolean);
  const toggled = company.split(",").map((item) => item.trim());
  const allActive = toggled.every((item) => companies.some((companyValue) => companyValue.toLowerCase() === item.toLowerCase()));
  for (const item of toggled) {
    const index = companies.findIndex((companyValue) => companyValue.toLowerCase() === item.toLowerCase());
    if (allActive && index >= 0) {
      companies.splice(index, 1);
    } else if (!allActive && index < 0) {
      companies.push(item);
    }
  }
  return companies.join(", ");
}

function PostingsTable({ postings, loading }: { postings: Posting[]; loading: boolean }) {
  if (loading) {
    return <EmptyState label="Loading postings" />;
  }
  if (postings.length === 0) {
    return <EmptyState label="No postings discovered" />;
  }

  return (
    <div className="table-wrap">
      <table className="postings-table">
        <thead>
          <tr>
            <th>Role</th>
            <th>Location</th>
            <th>Source</th>
            <th>Score</th>
            <th>Reasons</th>
            <th>Matched</th>
          </tr>
        </thead>
        <tbody>
          {postings.map((posting) => {
            const reasons = posting.match_reasons ?? [];
            const locations = splitPostingLocations(posting.location);
            const isEarlyStage = earlyStageCompanies.has(posting.company);
            return (
              <tr key={posting.id}>
                <td>
                  <strong>
                    <a className="posting-link" href={posting.url} target="_blank" rel="noreferrer">
                      {posting.title}
                      <ExternalLink size={13} />
                    </a>
                  </strong>
                  <div className="posting-company">
                    <span>{posting.company}</span>
                    {isEarlyStage && <span className="company-stage-badge">Early stage</span>}
                  </div>
                </td>
                <td className="posting-location">
                  {locations.length > 0 ? (
                    <>
                      <span>{locations.slice(0, 2).join("; ")}</span>
                      {locations.length > 2 && <small>+{locations.length - 2} more</small>}
                    </>
                  ) : (
                    "-"
                  )}
                </td>
                <td className="posting-source" title={posting.source_id || posting.id}>
                  <strong>{formatPostingSource(posting.source)}</strong>
                </td>
                <td className="posting-score">
                  <span className="score-badge">{posting.match_score ?? 0}</span>
                </td>
                <td>
                  {reasons.length > 0 ? (
                    <div className="reason-list">
                      {reasons.slice(0, 4).map((reason) => (
                        <span className="reason-pill" key={reason}>
                          {formatPostingReason(reason)}
                        </span>
                      ))}
                    </div>
                  ) : (
                    "-"
                  )}
                </td>
                <td>{relativeTime(posting.matched_at ?? posting.first_seen_at)}</td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

function splitPostingLocations(location?: string) {
  return (location ?? "")
    .split(";")
    .map((part) => part.trim())
    .filter(Boolean);
}

function formatPostingSource(source: string) {
  return source ? source.charAt(0).toUpperCase() + source.slice(1).toLowerCase() : "Unknown";
}

function formatPostingReason(reason: string) {
  const [category, ...valueParts] = reason.split(":");
  const value = valueParts.join(":").trim();
  if (!value) {
    return reason;
  }
  const label = category.trim().replaceAll("_", " ");
  return `${label.charAt(0).toUpperCase()}${label.slice(1)} · ${value}`;
}

function WorkflowsTable({
  workflows,
  runs,
  jobs,
  loading,
  onToggle,
  onRun,
  onDelete,
  onSelectJob
}: {
  workflows: Workflow[];
  runs: WorkflowRun[];
  jobs: Job[];
  loading: boolean;
  onToggle: (workflow: Workflow) => void;
  onRun: (workflow: Workflow) => void;
  onDelete: (workflow: Workflow) => void;
  onSelectJob: (id: string) => void;
}) {
  if (loading) {
    return <EmptyState label="Loading workflows" />;
  }
  if (workflows.length === 0) {
    return <EmptyState label="No scheduled workflows" />;
  }

  return (
    <div className="table-wrap">
      <table>
        <thead>
          <tr>
            <th>Workflow</th>
            <th>Job Type</th>
            <th>Status</th>
            <th>Interval</th>
            <th>Next Run</th>
            <th>Last Run</th>
            <th>Action</th>
          </tr>
        </thead>
        <tbody>
          {workflows.map((workflow) => {
            const recentRuns = runs
              .filter((run) => run.workflow_id === workflow.id)
              .sort((a, b) => new Date(b.created_at).getTime() - new Date(a.created_at).getTime())
              .slice(0, 3);

            return (
              <tr key={workflow.id}>
                <td>
                  <strong>{workflow.name}</strong>
                  <span>{workflow.id.slice(0, 12)}</span>
                </td>
                <td>{workflow.job_type}</td>
                <td>
                  <span className={`status-pill ${workflow.enabled ? "succeeded" : "canceled"}`}>
                    {workflow.enabled ? "enabled" : "disabled"}
                  </span>
                </td>
                <td>{formatInterval(workflow.interval_seconds)}</td>
                <td>{relativeTime(workflow.next_run_at)}</td>
                <td>
                  {recentRuns.length > 0 ? (
                    <div className="run-list">
                      {recentRuns.map((run) => {
                        const job = jobs.find((candidate) => candidate.id === run.job_id);
                        return (
                          <button
                            className="run-link"
                            type="button"
                            key={run.id}
                            onClick={() => onSelectJob(run.job_id)}
                            title="Open workflow run job detail"
                          >
                            <StatusPill status={job?.status ?? run.status} />
                            <span>{run.trigger}</span>
                            <span>{relativeTime(run.created_at)}</span>
                          </button>
                        );
                      })}
                    </div>
                  ) : workflow.last_run_at ? (
                    <>
                      <strong>{relativeTime(workflow.last_run_at)}</strong>
                      <span>{workflow.last_job_id ? workflow.last_job_id.slice(0, 12) : "-"}</span>
                    </>
                  ) : (
                    "-"
                  )}
                </td>
                <td>
                  <div className="table-actions">
                    <button
                      className="table-action"
                      type="button"
                      onClick={() => onRun(workflow)}
                      aria-label="Run workflow now"
                      title="Run workflow now"
                    >
                      <Play size={16} />
                      Run now
                    </button>
                    <button
                      className="table-action"
                      type="button"
                      onClick={() => onToggle(workflow)}
                      aria-label={workflow.enabled ? "Pause workflow" : "Resume workflow"}
                      title={workflow.enabled ? "Pause workflow" : "Resume workflow"}
                    >
                      {workflow.enabled ? <Pause size={16} /> : <ToggleRight size={16} />}
                      {workflow.enabled ? "Pause" : "Resume"}
                    </button>
                    {!workflow.enabled && (
                      <button
                        className="table-action danger"
                        type="button"
                        onClick={() => onDelete(workflow)}
                        aria-label="Delete workflow"
                        title="Delete disabled workflow"
                      >
                        <Trash2 size={16} />
                        Delete
                      </button>
                    )}
                  </div>
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

function ResultsTable({
  results,
  loading,
  onSelectJob
}: {
  results: Result[];
  loading: boolean;
  onSelectJob: (id: string) => void;
}) {
  if (loading) {
    return <EmptyState label="Loading results" />;
  }
  if (results.length === 0) {
    return <EmptyState label="No results recorded" />;
  }

  return (
    <div className="table-wrap">
      <table>
        <thead>
          <tr>
            <th>Result</th>
            <th>Workflow</th>
            <th>Job</th>
            <th>Alert</th>
            <th>Created</th>
          </tr>
        </thead>
        <tbody>
          {results.slice(0, 12).map((result) => (
            <tr key={result.id}>
              <td>
                <strong>{result.type}</strong>
                <span>{result.summary || result.id.slice(0, 12)}</span>
              </td>
              <td>{result.workflow_id ? result.workflow_id.slice(0, 12) : "-"}</td>
              <td>
                {result.job_id ? (
                  <button
                    className="link-button"
                    type="button"
                    onClick={() => onSelectJob(result.job_id as string)}
                    title="Open job detail"
                  >
                    {result.job_id.slice(0, 12)}
                  </button>
                ) : (
                  "-"
                )}
              </td>
              <td>
                {hasAlertFlag(result) ? (
                  <span className={`status-pill ${result.data?.alert_sent ? "succeeded" : "canceled"}`}>
                    {result.data?.alert_sent ? "sent" : "not sent"}
                  </span>
                ) : (
                  "-"
                )}
              </td>
              <td>{relativeTime(result.created_at)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function JobDetail({ job, results }: { job: Job; results: Result[] }) {
  const logs = job.logs ?? [];

  return (
    <div className="job-detail">
      <div className="detail-grid">
        <div>
          <span>Status</span>
          <StatusPill status={job.status} />
        </div>
        <div>
          <span>Attempts</span>
          <strong>
            {job.attempts}/{job.max_attempts}
          </strong>
        </div>
        <div>
          <span>Created</span>
          <strong>{relativeTime(job.created_at)}</strong>
        </div>
      </div>

      {job.error && <div className="error-box">{job.error}</div>}

      {results.length > 0 && (
        <div className="results-block">
          <div className="logs-title">
            <Terminal size={16} />
            Results
          </div>
          {results.slice(0, 3).map((result) => (
            <div className="result-line" key={result.id}>
              <strong>{result.type}</strong>
              <span>{result.summary || "No summary"}</span>
              {hasAlertFlag(result) && (
                <span className="result-meta">Alert {result.data?.alert_sent ? "sent" : "not sent"}</span>
              )}
            </div>
          ))}
        </div>
      )}

      <div className="logs">
        <div className="logs-title">
          <Terminal size={16} />
          Logs
        </div>
        {logs.length === 0 ? (
          <span className="muted">No logs</span>
        ) : (
          logs.slice(-6).map((entry) => (
            <div className="log-line" key={`${entry.time}-${entry.message}`}>
              <time>{formatTime(new Date(entry.time))}</time>
              <span>{entry.message}</span>
            </div>
          ))
        )}
      </div>
    </div>
  );
}

function StatusPill({ status }: { status: JobStatus }) {
  return <span className={`status-pill ${status}`}>{status}</span>;
}

function WorkerPill({ status }: { status: WorkerStatus }) {
  return <span className={`status-pill worker-${status}`}>{status}</span>;
}

function EmptyState({ label }: { label: string }) {
  return <div className="empty-state">{label}</div>;
}

function sortPostings(items: Posting[]) {
  return [...items].sort((a, b) => {
    const scoreDelta = (b.match_score ?? 0) - (a.match_score ?? 0);
    if (scoreDelta !== 0) {
      return scoreDelta;
    }
    return new Date(b.first_seen_at).getTime() - new Date(a.first_seen_at).getTime();
  });
}

function groupJobsByWorkflow(jobs: Job[]): JobTableRow[] {
  const rows = new Map<string, JobTableRow>();
  for (const job of jobs) {
    const workflowID = job.metadata?.workflow_id?.trim();
    const key = workflowID ? `workflow:${workflowID}` : `job:${job.id}`;
    const existing = rows.get(key);
    if (!existing) {
      rows.set(key, { job, executionCount: 1 });
      continue;
    }

    existing.executionCount += 1;
    if (new Date(job.updated_at).getTime() > new Date(existing.job.updated_at).getTime()) {
      existing.job = job;
    }
  }

  return Array.from(rows.values()).sort(
    (a, b) => new Date(b.job.updated_at).getTime() - new Date(a.job.updated_at).getTime()
  );
}

function jobRowsSubtitle(rowCount: number, executionCount: number) {
  if (rowCount === executionCount) {
    return `${executionCount} tracked executions`;
  }
  return `${rowCount} latest rows from ${executionCount} executions`;
}

function hasAlertFlag(result: Result) {
  return typeof result.data?.alert_sent === "boolean";
}

function formatAlertName(name: string) {
  return name
    .split("_")
    .filter(Boolean)
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join(" ");
}

function relativeTime(value: string) {
  const date = new Date(value);
  const seconds = Math.max(0, Math.round((Date.now() - date.getTime()) / 1000));
  if (seconds < 5) return "just now";
  if (seconds < 60) return `${seconds}s ago`;
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.round(minutes / 60);
  return `${hours}h ago`;
}

function formatTime(date: Date) {
  return date.toLocaleTimeString([], {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit"
  });
}

function formatInterval(seconds: number) {
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.round(minutes / 60);
  if (hours < 24) return `${hours}h`;
  const days = Math.round(hours / 24);
  return `${days}d`;
}
