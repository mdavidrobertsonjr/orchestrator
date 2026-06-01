const API_BASE_URL = import.meta.env.VITE_API_BASE_URL ?? "http://localhost:8080";

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

export async function fetchJobs(): Promise<Job[]> {
  const response = await request<{ jobs: Job[] }>("/v1/jobs");
  return response.jobs;
}

export async function fetchWorkers(): Promise<Worker[]> {
  const response = await request<{ workers: Worker[] }>("/v1/workers");
  return response.workers;
}

export function fetchQueue(): Promise<QueueStatus> {
  return request<QueueStatus>("/v1/queue");
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

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(`${API_BASE_URL}${path}`, init);
  if (!response.ok) {
    const body = await response.json().catch(() => null);
    const message = body?.error ?? `request failed with ${response.status}`;
    throw new Error(message);
  }
  return response.json() as Promise<T>;
}
