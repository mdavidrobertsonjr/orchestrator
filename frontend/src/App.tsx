import { FormEvent, useEffect, useMemo, useState } from "react";
import type { ReactNode } from "react";
import {
  Boxes,
  CheckCircle2,
  Clock3,
  Cpu,
  ExternalLink,
  LayoutDashboard,
  ListChecks,
  Pause,
  Play,
  RefreshCw,
  Server,
  Send,
  Terminal,
  ToggleRight,
  XCircle
} from "lucide-react";
import {
  cancelJob,
  createJob,
  createNaturalCommand,
  createWorkflow,
  fetchHealth,
  fetchJobs,
  fetchMetrics,
  fetchPostings,
  fetchQueue,
  fetchResults,
  fetchWorkflowRuns,
  fetchWorkflows,
  fetchWorkers,
  Job,
  JobStatus,
  Posting,
  QueueStatus,
  Result,
  retryJob,
  RuntimeMetrics,
  runWorkflow,
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
      min_score: 20,
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
  const [commandPrompt, setCommandPrompt] = useState(
    "Monitor Datadog new-grad software engineering roles in NYC every day"
  );
  const [commandResult, setCommandResult] = useState<string | null>(null);
  const [selectedJobID, setSelectedJobID] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [apiOnline, setApiOnline] = useState(false);
  const [lastUpdated, setLastUpdated] = useState<Date | null>(null);
  const [activeSection, setActiveSection] = useState<DashboardSection>("overview");

  const selectedJob = useMemo(
    () => jobs.find((job) => job.id === selectedJobID) ?? jobs[0],
    [jobs, selectedJobID]
  );

  const selectedJobResults = useMemo(
    () => (selectedJob ? results.filter((result) => result.job_id === selectedJob.id) : []),
    [results, selectedJob]
  );

  const activeNavItem = navItems.find((item) => item.id === activeSection) ?? navItems[0];

  const summary = useMemo(() => {
    return {
      queued: jobs.filter((job) => job.status === "queued").length,
      running: jobs.filter((job) => job.status === "running").length,
      succeeded: jobs.filter((job) => job.status === "succeeded").length,
      failed: jobs.filter((job) => job.status === "failed").length,
      postings: postings.length,
      results: results.length,
      workflows: workflows.length,
      activeWorkers: workers.filter((worker) => worker.status !== "stopped").length
    };
  }, [jobs, postings, results, workflows, workers]);

  async function refresh() {
    try {
      const [, nextJobs, nextWorkers, nextQueue, nextMetrics, nextPostings, nextWorkflows, nextRuns, nextResults] =
        await Promise.all([
          fetchHealth(),
          fetchJobs(),
          fetchWorkers(),
          fetchQueue(),
          fetchMetrics(),
          fetchPostings(),
          fetchWorkflows(),
          fetchWorkflowRuns(),
          fetchResults()
        ]);
      setJobs(nextJobs);
      setWorkers(nextWorkers);
      setQueue(nextQueue);
      setMetrics(nextMetrics);
      setPostings(nextPostings);
      setWorkflows(nextWorkflows);
      setWorkflowRuns(nextRuns);
      setResults(nextResults);
      setApiOnline(true);
      setLastUpdated(new Date());
      setError(null);
    } catch (err) {
      setApiOnline(false);
      setError(err instanceof Error ? err.message : "failed to refresh dashboard");
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    void refresh();
    const interval = window.setInterval(() => void refresh(), 2500);
    return () => window.clearInterval(interval);
  }, []);

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

  const overviewPanels = (
    <>
      <SummaryGrid summary={summary} />
      <section className="dashboard-grid">
        <Panel title="Recent Jobs" subtitle={`${jobs.length} tracked executions`}>
          <JobsTable
            jobs={jobs.slice(0, 8)}
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
        <Panel title="Jobs" subtitle={`${jobs.length} tracked executions`}>
          <JobsTable
            jobs={jobs}
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
      <Panel title="Discovered Postings" subtitle={`${postings.length} monitor matches`}>
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
          onSelectJob={setSelectedJobID}
        />
      </Panel>
    ),
    results: (
      <Panel title="Recent Results" subtitle={`${results.length} structured outputs`}>
        <ResultsTable results={results} loading={loading} onSelectJob={setSelectedJobID} />
      </Panel>
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
      </section>
    ),
    commands: (
      <section className="command-grid">
        <Panel title="Tell Orchestrator What To Do" subtitle="Create a job or workflow from one request">
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
  jobs,
  selectedJobID,
  onSelect,
  onCancel,
  onRetry,
  loading
}: {
  jobs: Job[];
  selectedJobID?: string;
  onSelect: (id: string) => void;
  onCancel: (job: Job) => void;
  onRetry: (job: Job) => void;
  loading: boolean;
}) {
  if (loading) {
    return <EmptyState label="Loading jobs" />;
  }
  if (jobs.length === 0) {
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
          {jobs.map((job) => (
            <tr
              className={job.id === selectedJobID ? "selected" : ""}
              key={job.id}
              onClick={() => onSelect(job.id)}
            >
              <td>
                <strong>{job.name}</strong>
                <span>{job.id.slice(0, 12)}</span>
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

function PostingsTable({ postings, loading }: { postings: Posting[]; loading: boolean }) {
  if (loading) {
    return <EmptyState label="Loading postings" />;
  }
  if (postings.length === 0) {
    return <EmptyState label="No postings discovered" />;
  }

  return (
    <div className="table-wrap">
      <table>
        <thead>
          <tr>
            <th>Role</th>
            <th>Location</th>
            <th>Source</th>
            <th>Score</th>
            <th>Matched</th>
          </tr>
        </thead>
        <tbody>
          {postings.map((posting) => (
            <tr key={posting.id}>
              <td>
                <strong>
                  <a className="posting-link" href={posting.url} target="_blank" rel="noreferrer">
                    {posting.title}
                    <ExternalLink size={13} />
                  </a>
                </strong>
                <span>{posting.company}</span>
              </td>
              <td>{posting.location || "-"}</td>
              <td>
                <strong>{posting.source}</strong>
                <span>{posting.source_id || posting.id.slice(0, 12)}</span>
              </td>
              <td>{posting.match_score ?? 0}</td>
              <td>{relativeTime(posting.matched_at ?? posting.first_seen_at)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function WorkflowsTable({
  workflows,
  runs,
  jobs,
  loading,
  onToggle,
  onRun,
  onSelectJob
}: {
  workflows: Workflow[];
  runs: WorkflowRun[];
  jobs: Job[];
  loading: boolean;
  onToggle: (workflow: Workflow) => void;
  onRun: (workflow: Workflow) => void;
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

function hasAlertFlag(result: Result) {
  return typeof result.data?.alert_sent === "boolean";
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
