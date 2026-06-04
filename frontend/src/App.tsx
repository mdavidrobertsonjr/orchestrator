import { FormEvent, useEffect, useMemo, useState } from "react";
import type { ReactNode } from "react";
import {
  Activity,
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
  createJob,
  createNaturalJob,
  createWorkflow,
  fetchHealth,
  fetchJobs,
  fetchPostings,
  fetchQueue,
  fetchWorkflows,
  fetchWorkers,
  Job,
  JobStatus,
  Posting,
  QueueStatus,
  updateWorkflow,
  Worker,
  WorkerStatus,
  Workflow
} from "./api";

const defaultQueue: QueueStatus = { queued: 0, capacity: 0 };

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
  const [workflows, setWorkflows] = useState<Workflow[]>([]);
  const [workers, setWorkers] = useState<Worker[]>([]);
  const [queue, setQueue] = useState<QueueStatus>(defaultQueue);
  const [form, setForm] = useState<SubmitState>(initialSubmitState);
  const [workflowForm, setWorkflowForm] = useState<WorkflowFormState>(initialWorkflowForm);
  const [naturalPrompt, setNaturalPrompt] = useState("Email me a summary of failed jobs every morning at 8am");
  const [selectedJobID, setSelectedJobID] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [apiOnline, setApiOnline] = useState(false);
  const [lastUpdated, setLastUpdated] = useState<Date | null>(null);

  const selectedJob = useMemo(
    () => jobs.find((job) => job.id === selectedJobID) ?? jobs[0],
    [jobs, selectedJobID]
  );

  const summary = useMemo(() => {
    return {
      queued: jobs.filter((job) => job.status === "queued").length,
      running: jobs.filter((job) => job.status === "running").length,
      succeeded: jobs.filter((job) => job.status === "succeeded").length,
      failed: jobs.filter((job) => job.status === "failed").length,
      postings: postings.length,
      workflows: workflows.length,
      activeWorkers: workers.filter((worker) => worker.status !== "stopped").length
    };
  }, [jobs, postings, workflows, workers]);

  async function refresh() {
    try {
      const [, nextJobs, nextWorkers, nextQueue, nextPostings, nextWorkflows] = await Promise.all([
        fetchHealth(),
        fetchJobs(),
        fetchWorkers(),
        fetchQueue(),
        fetchPostings(),
        fetchWorkflows()
      ]);
      setJobs(nextJobs);
      setWorkers(nextWorkers);
      setQueue(nextQueue);
      setPostings(nextPostings);
      setWorkflows(nextWorkflows);
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

  async function handleNaturalSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setSubmitting(true);
    setError(null);

    try {
      const created = await createNaturalJob({ prompt: naturalPrompt.trim() });
      setSelectedJobID(created.id);
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : "failed to submit natural language job");
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
          <a className="nav-item active" href="#overview">
            <LayoutDashboard size={18} />
            Overview
          </a>
          <a className="nav-item" href="#jobs">
            <ListChecks size={18} />
            Jobs
          </a>
          <a className="nav-item" href="#postings">
            <ExternalLink size={18} />
            Postings
          </a>
          <a className="nav-item" href="#workflows">
            <Clock3 size={18} />
            Workflows
          </a>
          <a className="nav-item" href="#workers">
            <Server size={18} />
            Workers
          </a>
          <a className="nav-item" href="#queue">
            <Activity size={18} />
            Queue
          </a>
        </nav>
      </aside>

      <main className="main">
        <header className="topbar">
          <div>
            <p className="eyebrow">Distributed Job Runtime</p>
            <h1>Operations Overview</h1>
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

        <section id="overview" className="summary-grid" aria-label="Summary">
          <SummaryCard label="Queued" value={summary.queued} icon={<Clock3 size={20} />} tone="queued" />
          <SummaryCard label="Running" value={summary.running} icon={<Play size={20} />} tone="running" />
          <SummaryCard label="Succeeded" value={summary.succeeded} icon={<CheckCircle2 size={20} />} tone="succeeded" />
          <SummaryCard label="Failed" value={summary.failed} icon={<XCircle size={20} />} tone="failed" />
          <SummaryCard label="Postings" value={summary.postings} icon={<ExternalLink size={20} />} tone="postings" />
          <SummaryCard label="Workflows" value={summary.workflows} icon={<Clock3 size={20} />} tone="workflows" />
          <SummaryCard label="Workers" value={summary.activeWorkers} icon={<Cpu size={20} />} tone="workers" />
        </section>

        <section className="content-grid">
          <div className="primary-column">
            <section id="jobs" className="panel">
              <div className="panel-header">
                <div>
                  <h2>Jobs</h2>
                  <p>{jobs.length} tracked executions</p>
                </div>
              </div>
              <JobsTable jobs={jobs} selectedJobID={selectedJob?.id} onSelect={setSelectedJobID} loading={loading} />
            </section>

            <section id="postings" className="panel">
              <div className="panel-header">
                <div>
                  <h2>Discovered Postings</h2>
                  <p>{postings.length} monitor matches</p>
                </div>
              </div>
              <PostingsTable postings={postings} loading={loading} />
            </section>

            <section id="workflows" className="panel">
              <div className="panel-header">
                <div>
                  <h2>Scheduled Workflows</h2>
                  <p>{workflows.length} recurring definitions</p>
                </div>
              </div>
              <WorkflowsTable workflows={workflows} loading={loading} onToggle={(workflow) => void handleWorkflowToggle(workflow)} />
            </section>

            <section id="workers" className="panel">
              <div className="panel-header">
                <div>
                  <h2>Workers</h2>
                  <p>{workers.length} registered nodes</p>
                </div>
              </div>
              <WorkersTable workers={workers} loading={loading} />
            </section>
          </div>

          <aside className="side-column">
            <section className="panel submit-panel">
              <div className="panel-header">
                <div>
                  <h2>Schedule Workflow</h2>
                  <p>Create recurring work</p>
                </div>
              </div>
              <form className="submit-form" onSubmit={handleWorkflowSubmit}>
                <label>
                  <span>Name</span>
                  <input
                    value={workflowForm.name}
                    onChange={(event) => setWorkflowForm({ ...workflowForm, name: event.target.value })}
                    placeholder="datadog-new-grad-monitor"
                  />
                </label>
                <label>
                  <span>Job Type</span>
                  <select
                    value={workflowForm.jobType}
                    onChange={(event) => setWorkflowForm({ ...workflowForm, jobType: event.target.value })}
                  >
                    <option value="jobs.monitor.new_grad">jobs.monitor.new_grad</option>
                    <option value="report.email">report.email</option>
                    <option value="video.transcode">video.transcode</option>
                    <option value="scrape.url">scrape.url</option>
                    <option value="data.pipeline">data.pipeline</option>
                  </select>
                </label>
                <div className="field-row">
                  <label>
                    <span>Interval</span>
                    <select
                      value={workflowForm.intervalSeconds}
                      onChange={(event) =>
                        setWorkflowForm({ ...workflowForm, intervalSeconds: Number(event.target.value) })
                      }
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
                      value={workflowForm.maxAttempts}
                      onChange={(event) =>
                        setWorkflowForm({ ...workflowForm, maxAttempts: Number(event.target.value) })
                      }
                    />
                  </label>
                </div>
                <label>
                  <span>Payload JSON</span>
                  <textarea
                    className="code-textarea"
                    value={workflowForm.payload}
                    rows={10}
                    spellCheck={false}
                    onChange={(event) => setWorkflowForm({ ...workflowForm, payload: event.target.value })}
                  />
                </label>
                <label className="checkbox-field">
                  <input
                    type="checkbox"
                    checked={workflowForm.enabled}
                    onChange={(event) => setWorkflowForm({ ...workflowForm, enabled: event.target.checked })}
                  />
                  <span>Enabled</span>
                </label>
                <button
                  className="primary-button"
                  type="submit"
                  disabled={submitting || workflowForm.jobType.trim() === ""}
                >
                  <Send size={16} />
                  {submitting ? "Scheduling" : "Schedule Workflow"}
                </button>
              </form>
            </section>

            <section className="panel submit-panel">
              <div className="panel-header">
                <div>
                  <h2>Ask Orchestrator</h2>
                  <p>Describe a job in English</p>
                </div>
              </div>
              <form className="submit-form" onSubmit={handleNaturalSubmit}>
                <label>
                  <span>Request</span>
                  <textarea
                    value={naturalPrompt}
                    onChange={(event) => setNaturalPrompt(event.target.value)}
                    rows={4}
                  />
                </label>
                <button className="primary-button" type="submit" disabled={submitting || naturalPrompt.trim() === ""}>
                  <Send size={16} />
                  {submitting ? "Planning" : "Plan & Submit"}
                </button>
              </form>
            </section>

            <section className="panel submit-panel">
              <div className="panel-header">
                <div>
                  <h2>Submit Job</h2>
                  <p>Create a simulated workload</p>
                </div>
              </div>
              <form className="submit-form" onSubmit={handleSubmit}>
                <label>
                  <span>Name</span>
                  <input
                    value={form.name}
                    onChange={(event) => setForm({ ...form, name: event.target.value })}
                    placeholder="demo-transcode"
                  />
                </label>
                <label>
                  <span>Type</span>
                  <select value={form.type} onChange={(event) => setForm({ ...form, type: event.target.value })}>
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
                      onChange={(event) => setForm({ ...form, durationMs: Number(event.target.value) })}
                    />
                  </label>
                  <label>
                    <span>Attempts</span>
                    <input
                      type="number"
                      min={1}
                      max={10}
                      value={form.maxAttempts}
                      onChange={(event) => setForm({ ...form, maxAttempts: Number(event.target.value) })}
                    />
                  </label>
                </div>
                <label className="checkbox-field">
                  <input
                    type="checkbox"
                    checked={form.shouldFail}
                    onChange={(event) => setForm({ ...form, shouldFail: event.target.checked })}
                  />
                  <span>Simulate failure</span>
                </label>
                <button className="primary-button" type="submit" disabled={submitting || form.type.trim() === ""}>
                  <Send size={16} />
                  {submitting ? "Submitting" : "Submit Job"}
                </button>
              </form>
            </section>

            <section id="queue" className="panel queue-panel">
              <div className="panel-header">
                <div>
                  <h2>Queue</h2>
                  <p>In-memory capacity</p>
                </div>
              </div>
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
                <div className="meter-fill" style={{ width: `${queue.capacity ? (queue.queued / queue.capacity) * 100 : 0}%` }} />
              </div>
            </section>

            <section className="panel detail-panel">
              <div className="panel-header">
                <div>
                  <h2>Job Detail</h2>
                  <p>{selectedJob ? selectedJob.id.slice(0, 12) : "No job selected"}</p>
                </div>
              </div>
              {selectedJob ? <JobDetail job={selectedJob} /> : <EmptyState label="No jobs yet" />}
            </section>
          </aside>
        </section>
      </main>
    </div>
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
  loading
}: {
  jobs: Job[];
  selectedJobID?: string;
  onSelect: (id: string) => void;
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
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
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
  loading,
  onToggle
}: {
  workflows: Workflow[];
  loading: boolean;
  onToggle: (workflow: Workflow) => void;
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
          {workflows.map((workflow) => (
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
                {workflow.last_run_at ? (
                  <>
                    <strong>{relativeTime(workflow.last_run_at)}</strong>
                    <span>{workflow.last_job_id ? workflow.last_job_id.slice(0, 12) : "-"}</span>
                  </>
                ) : (
                  "-"
                )}
              </td>
              <td>
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
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function JobDetail({ job }: { job: Job }) {
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
