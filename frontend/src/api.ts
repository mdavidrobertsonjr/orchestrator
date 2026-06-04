const API_BASE_URL = import.meta.env.VITE_API_BASE_URL ?? "";

export type JobStatus = "queued" | "running" | "succeeded" | "failed" | "canceled";

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
  match_score?: number;
  match_reasons?: string[];
  metadata?: Record<string, string>;
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

export async function fetchJobs(): Promise<Job[]> {
  const response = await request<{ jobs: Job[] }>("/v1/jobs");
  return response.jobs;
}

export async function fetchWorkers(): Promise<Worker[]> {
  const response = await request<{ workers: Worker[] }>("/v1/workers");
  return response.workers;
}

export async function fetchPostings(): Promise<Posting[]> {
  const response = await request<{ postings: Posting[] }>("/v1/postings");
  return response.postings;
}

export async function fetchWorkflows(): Promise<Workflow[]> {
  const response = await request<{ workflows: Workflow[] }>("/v1/workflows");
  return response.workflows;
}

export function fetchQueue(): Promise<QueueStatus> {
  return request<QueueStatus>("/v1/queue");
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

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(`${API_BASE_URL}${path}`, init);
  if (!response.ok) {
    const body = await response.json().catch(() => null);
    const message = body?.error ?? `request failed with ${response.status}`;
    throw new Error(message);
  }
  return response.json() as Promise<T>;
}
