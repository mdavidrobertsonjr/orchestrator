let API_BASE_URL = import.meta.env.VITE_API_BASE_URL ?? "";
const AUTH_STORAGE_KEY = "orchestrator.authToken";
let hostedMode = false;

export function useHostedAccounts(): void {
  hostedMode = true;
  API_BASE_URL = "";
  clearAuthToken();
}

export function eventStreamURL(): string {
  const token = getAuthToken();
  const suffix = token ? `?auth_token=${encodeURIComponent(token)}` : "";
  return `${API_BASE_URL}/v1/events${suffix}`;
}

export function getAuthToken(): string {
  if (typeof window === "undefined") {
    return "";
  }
  return window.localStorage.getItem(AUTH_STORAGE_KEY) ?? "";
}

export function setAuthToken(token: string): void {
  if (typeof window === "undefined") {
    return;
  }
  const trimmed = token.trim();
  if (trimmed === "") {
    window.localStorage.removeItem(AUTH_STORAGE_KEY);
    return;
  }
  window.localStorage.setItem(AUTH_STORAGE_KEY, trimmed);
}

export function clearAuthToken(): void {
  setAuthToken("");
}

export type JobStatus = "queued" | "running" | "succeeded" | "failed" | "dead_letter" | "canceled";

export type Job = {
  id: string;
  name: string;
  type: string;
  status: JobStatus;
  payload?: Record<string, unknown>;
  attempts: number;
  max_attempts: number;
  error?: string;
  logs?: JobLog[];
  created_at: string;
  updated_at: string;
  started_at?: string;
  finished_at?: string;
  metadata?: Record<string, string>;
};

export type JobLog = {
  time: string;
  message: string;
};

export type WorkerStatus = "idle" | "running" | "stopped";

export type Worker = {
  id: string;
  status: WorkerStatus;
  current_job_id?: string;
  last_heartbeat: string;
  started_at: string;
  updated_at: string;
};

export type QueueStatus = {
  queued: number;
  capacity: number;
};

export type RuntimeMetrics = {
  generated_at: string;
  queue: {
    queued: number;
    capacity: number;
    utilization: number;
  };
  jobs: {
    total: number;
    by_status: Partial<Record<JobStatus, number>>;
    attempts: number;
    retry_attempts: number;
    leased: number;
    expired_leases: number;
  };
  workers: {
    total: number;
    by_status: Partial<Record<WorkerStatus, number>>;
    active: number;
    running: number;
    heartbeat: number;
  };
  workflows: {
    total: number;
    enabled: number;
    disabled: number;
    due: number;
    run_records: number;
  };
  postings: {
    total: number;
  };
  results: {
    total: number;
  };
  notifications?: {
    total: number;
    failed?: number;
  };
  alerts?: OperationalAlert[];
};

export type OperationalAlert = {
  severity: "warning" | "critical" | string;
  name: string;
  message: string;
  value: number;
};

export type Posting = {
  id: string;
  company: string;
  title: string;
  url: string;
  location?: string;
  source: string;
  source_id?: string;
  dedupe_key: string;
  posted_at?: string;
  first_seen_at: string;
  last_seen_at: string;
  matched_at?: string;
  applied_at?: string;
  match_score?: number;
  match_reasons?: string[];
  metadata?: Record<string, string>;
};

export type PostingFilters = {
  q?: string;
  company?: string;
  source?: string;
  location?: string;
  minScore?: number;
  applied?: "applied" | "not_applied";
  freshness?: "current" | "stale";
  pageSize?: number;
};

export type Workflow = {
  id: string;
  name: string;
  job_type: string;
  payload?: Record<string, unknown>;
  metadata?: Record<string, string>;
  max_attempts: number;
  enabled: boolean;
  interval_seconds: number;
  next_run_at: string;
  last_run_at?: string;
  last_job_id?: string;
  created_at: string;
  updated_at: string;
};

export type WorkflowRun = {
  id: string;
  workflow_id: string;
  job_id: string;
  trigger: string;
  status: JobStatus;
  metadata?: Record<string, string>;
  created_at: string;
  updated_at: string;
};

export type Result = {
  id: string;
  job_id?: string;
  workflow_id?: string;
  type: string;
  summary?: string;
  data?: Record<string, unknown>;
  created_at: string;
  updated_at: string;
};

export type HealthStatus = {
  status: string;
};

export type CreateJobInput = {
  name: string;
  type: string;
  max_attempts: number;
  payload: {
    duration_ms: number;
    should_fail: boolean;
  };
  metadata: Record<string, string>;
};

export type CreateNaturalJobInput = {
  prompt: string;
};

export type CreateWorkflowInput = {
  name: string;
  job_type: string;
  max_attempts: number;
  enabled: boolean;
  interval_seconds: number;
  payload: Record<string, unknown>;
  metadata: Record<string, string>;
};

export type UpdateWorkflowInput = {
  enabled: boolean;
};

export type NaturalCommandResponse = {
  action: "job" | "workflow";
  job?: Job;
  workflow?: Workflow;
};

export async function fetchJobs(): Promise<Job[]> {
  const response = await request<{ jobs: Job[] | null }>("/v1/jobs");
  return response.jobs ?? [];
}

export async function fetchWorkers(): Promise<Worker[]> {
  const response = await request<{ workers: Worker[] | null }>("/v1/workers");
  return response.workers ?? [];
}

export async function fetchPostings(filters: PostingFilters = {}): Promise<Posting[]> {
  const params = new URLSearchParams();
  appendQuery(params, "q", filters.q);
  appendQuery(params, "company", filters.company);
  appendQuery(params, "source", filters.source);
  appendQuery(params, "location", filters.location);
  appendQuery(params, "applied", filters.applied);
  appendQuery(params, "freshness", filters.freshness);
  if (filters.minScore && filters.minScore > 0) {
    params.set("min_score", String(filters.minScore));
  }
  if (filters.pageSize && filters.pageSize > 0) {
    params.set("page_size", String(filters.pageSize));
  }
  const query = params.toString();
  const response = await request<{ postings: Posting[] | null }>(`/v1/postings${query ? `?${query}` : ""}`);
  return response.postings ?? [];
}

export function updatePostingApplied(id: string, applied: boolean): Promise<Posting> {
  return request<Posting>(`/v1/postings/${id}`, {
    method: "PATCH",
    headers: {
      "Content-Type": "application/json"
    },
    body: JSON.stringify({ applied })
  });
}

export function deletePosting(id: string): Promise<void> {
  return request<void>(`/v1/postings/${id}`, {
    method: "DELETE"
  });
}

function appendQuery(params: URLSearchParams, key: string, value?: string): void {
  const trimmed = value?.trim();
  if (trimmed) {
    params.set(key, trimmed);
  }
}

export async function fetchWorkflows(): Promise<Workflow[]> {
  const response = await request<{ workflows: Workflow[] | null }>("/v1/workflows");
  return response.workflows ?? [];
}

export async function fetchWorkflowRuns(): Promise<WorkflowRun[]> {
  const response = await request<{ runs: WorkflowRun[] | null }>("/v1/workflow-runs");
  return response.runs ?? [];
}

export async function fetchResults(): Promise<Result[]> {
  const response = await request<{ results: Result[] | null }>("/v1/results");
  return response.results ?? [];
}

export function fetchQueue(): Promise<QueueStatus> {
  return request<QueueStatus>("/v1/queue");
}

export function fetchMetrics(): Promise<RuntimeMetrics> {
  return request<RuntimeMetrics>("/v1/metrics");
}

export function fetchHealth(): Promise<HealthStatus> {
  return request<HealthStatus>("/healthz");
}

export function createJob(input: CreateJobInput): Promise<Job> {
  return request<Job>("/v1/jobs", {
    method: "POST",
    headers: {
      "Content-Type": "application/json"
    },
    body: JSON.stringify(input)
  });
}

export function createNaturalJob(input: CreateNaturalJobInput): Promise<Job> {
  return request<Job>("/v1/jobs/natural", {
    method: "POST",
    headers: {
      "Content-Type": "application/json"
    },
    body: JSON.stringify(input)
  });
}

export function createWorkflow(input: CreateWorkflowInput): Promise<Workflow> {
  return request<Workflow>("/v1/workflows", {
    method: "POST",
    headers: {
      "Content-Type": "application/json"
    },
    body: JSON.stringify(input)
  });
}

export function createNaturalWorkflow(input: CreateNaturalJobInput): Promise<Workflow> {
  return request<Workflow>("/v1/workflows/natural", {
    method: "POST",
    headers: {
      "Content-Type": "application/json"
    },
    body: JSON.stringify(input)
  });
}

export function createNaturalCommand(input: CreateNaturalJobInput): Promise<NaturalCommandResponse> {
  return request<NaturalCommandResponse>("/v1/commands/natural", {
    method: "POST",
    headers: {
      "Content-Type": "application/json"
    },
    body: JSON.stringify(input)
  });
}

export function updateWorkflow(id: string, input: UpdateWorkflowInput): Promise<Workflow> {
  return request<Workflow>(`/v1/workflows/${id}`, {
    method: "PATCH",
    headers: {
      "Content-Type": "application/json"
    },
    body: JSON.stringify(input)
  });
}

export function deleteWorkflow(id: string): Promise<void> {
  return request<void>(`/v1/workflows/${id}`, { method: "DELETE" });
}

export function runWorkflow(id: string): Promise<Job> {
  return request<Job>(`/v1/workflows/${id}/run`, {
    method: "POST"
  });
}

export function cancelJob(id: string): Promise<Job> {
  return request<Job>(`/v1/jobs/${id}/cancel`, {
    method: "POST"
  });
}

export function retryJob(id: string): Promise<Job> {
  return request<Job>(`/v1/jobs/${id}/retry`, {
    method: "POST"
  });
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const headers = new Headers(init?.headers);
  const token = getAuthToken();
  if (token !== "" && !headers.has("Authorization")) {
    headers.set("Authorization", `Bearer ${token}`);
  }

  const response = await fetch(`${API_BASE_URL}${path}`, {
    ...init,
    headers
  });
  if (!response.ok) {
    if (hostedMode && response.status === 401) {
      window.dispatchEvent(new Event("orchestrator:session-expired"));
    }
    const body = await response.json().catch(() => null);
    const message = body?.error ?? `request failed with ${response.status}`;
    throw new Error(message);
  }
  if (response.status === 204) {
    return undefined as T;
  }
  return response.json() as Promise<T>;
}
