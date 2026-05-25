package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"orchestrator/backend/internal/jobs"
	"orchestrator/backend/internal/workers"
)

func TestPoolExecutesJobSuccessfully(t *testing.T) {
	store := jobs.NewMemoryStore()
	queue := jobs.NewMemoryQueue(2)
	registry := workers.NewMemoryRegistry()
	executor := &fakeExecutor{}

	job, err := store.Create(jobs.CreateJobParams{Name: "demo", Type: "demo.sleep"})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if err := queue.Enqueue(t.Context(), job.ID); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	pool := NewPool(PoolConfig{WorkerCount: 1, PollDelay: time.Millisecond, HeartbeatInterval: time.Millisecond}, queue, store, executor, registry, testLogger())
	pool.Start(ctx)
	defer func() {
		cancel()
		pool.Wait()
	}()

	got := waitForJobStatus(t, store, job.ID, jobs.StatusSucceeded)
	if got.Attempts != 1 {
		t.Fatalf("expected 1 attempt, got %d", got.Attempts)
	}

	worker := waitForWorkerStatus(t, registry, "worker-1", workers.StatusIdle)
	if worker.CurrentJobID != "" {
		t.Fatalf("expected worker to clear current job, got %q", worker.CurrentJobID)
	}
}

func TestPoolRetriesThenSucceeds(t *testing.T) {
	store := jobs.NewMemoryStore()
	queue := jobs.NewMemoryQueue(2)
	registry := workers.NewMemoryRegistry()
	executor := &fakeExecutor{
		outcomes: []error{errors.New("temporary failure"), nil},
	}

	job, err := store.Create(jobs.CreateJobParams{
		Name:        "demo",
		Type:        "demo.sleep",
		MaxAttempts: 2,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if err := queue.Enqueue(t.Context(), job.ID); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	pool := NewPool(PoolConfig{WorkerCount: 1, PollDelay: time.Millisecond, HeartbeatInterval: time.Millisecond}, queue, store, executor, registry, testLogger())
	pool.Start(ctx)
	defer func() {
		cancel()
		pool.Wait()
	}()

	got := waitForJobStatus(t, store, job.ID, jobs.StatusSucceeded)
	if got.Attempts != 2 {
		t.Fatalf("expected 2 attempts, got %d", got.Attempts)
	}
	if !hasLog(got, "job attempt failed; requeueing: temporary failure") {
		t.Fatalf("expected retry log, got %#v", got.Logs)
	}
}

func TestPoolMarksJobFailedAfterAttemptsExhausted(t *testing.T) {
	store := jobs.NewMemoryStore()
	queue := jobs.NewMemoryQueue(2)
	registry := workers.NewMemoryRegistry()
	executor := &fakeExecutor{
		outcomes: []error{errors.New("boom"), errors.New("boom")},
	}

	job, err := store.Create(jobs.CreateJobParams{
		Name:        "demo",
		Type:        "demo.sleep",
		MaxAttempts: 2,
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if err := queue.Enqueue(t.Context(), job.ID); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	pool := NewPool(PoolConfig{WorkerCount: 1, PollDelay: time.Millisecond, HeartbeatInterval: time.Millisecond}, queue, store, executor, registry, testLogger())
	pool.Start(ctx)
	defer func() {
		cancel()
		pool.Wait()
	}()

	got := waitForJobStatus(t, store, job.ID, jobs.StatusFailed)
	if got.Attempts != 2 {
		t.Fatalf("expected 2 attempts, got %d", got.Attempts)
	}
	if got.Error != "boom" {
		t.Fatalf("expected final error boom, got %q", got.Error)
	}
}

func TestPoolRegistersAndStopsWorkers(t *testing.T) {
	store := jobs.NewMemoryStore()
	queue := jobs.NewMemoryQueue(2)
	registry := workers.NewMemoryRegistry()
	executor := &fakeExecutor{}

	ctx, cancel := context.WithCancel(context.Background())
	pool := NewPool(PoolConfig{WorkerCount: 1, PollDelay: time.Millisecond, HeartbeatInterval: time.Millisecond}, queue, store, executor, registry, testLogger())
	pool.Start(ctx)

	waitForWorkerStatus(t, registry, "worker-1", workers.StatusIdle)

	cancel()
	pool.Wait()

	worker, err := registry.Get("worker-1")
	if err != nil {
		t.Fatalf("get worker: %v", err)
	}
	if worker.Status != workers.StatusStopped {
		t.Fatalf("expected stopped status, got %q", worker.Status)
	}
}

type fakeExecutor struct {
	mu       sync.Mutex
	outcomes []error
	calls    int
}

func (e *fakeExecutor) Execute(ctx context.Context, job *jobs.Job, logf func(string)) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.calls++
	logf("fake executor called")

	if len(e.outcomes) == 0 {
		return nil
	}

	outcome := e.outcomes[0]
	e.outcomes = e.outcomes[1:]
	return outcome
}

func waitForJobStatus(t *testing.T, store jobs.Store, jobID string, status jobs.Status) *jobs.Job {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		job, err := store.Get(jobID)
		if err != nil {
			t.Fatalf("get job: %v", err)
		}
		if job.Status == status {
			return job
		}
		time.Sleep(10 * time.Millisecond)
	}

	job, err := store.Get(jobID)
	if err != nil {
		t.Fatalf("get job after timeout: %v", err)
	}
	t.Fatalf("timed out waiting for status %q; last status %q", status, job.Status)
	return nil
}

func waitForWorkerStatus(t *testing.T, registry workers.Registry, workerID string, status workers.Status) *workers.Worker {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		worker, err := registry.Get(workerID)
		if err == nil && worker.Status == status {
			return worker
		}
		time.Sleep(10 * time.Millisecond)
	}

	worker, err := registry.Get(workerID)
	if err != nil {
		t.Fatalf("get worker after timeout: %v", err)
	}
	t.Fatalf("timed out waiting for worker status %q; last status %q", status, worker.Status)
	return nil
}

func hasLog(job *jobs.Job, message string) bool {
	for _, entry := range job.Logs {
		if entry.Message == message {
			return true
		}
	}
	return false
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
