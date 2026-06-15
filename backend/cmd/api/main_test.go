package main

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"orchestrator/backend/internal/jobs"
)

func TestBuildQueueDefaultsToMemoryWithoutDatabase(t *testing.T) {
	queue, hydrate := buildQueue(config{QueueSize: 3}, jobs.NewMemoryStore(), testLogger())
	if _, ok := queue.(*jobs.MemoryQueue); !ok {
		t.Fatalf("expected memory queue, got %T", queue)
	}
	if !hydrate {
		t.Fatal("expected memory queue to require startup hydration")
	}
	if queue.Cap() != 3 {
		t.Fatalf("expected configured capacity 3, got %d", queue.Cap())
	}
}

func TestBuildQueueDefaultsToStoreWithDatabase(t *testing.T) {
	queue, hydrate := buildQueue(config{DatabaseURL: "postgres://example"}, jobs.NewMemoryStore(), testLogger())
	if _, ok := queue.(*jobs.StoreQueue); !ok {
		t.Fatalf("expected store queue, got %T", queue)
	}
	if hydrate {
		t.Fatal("did not expect store queue to require startup hydration")
	}
}

func TestBuildQueueHonorsExplicitMemoryBackend(t *testing.T) {
	queue, hydrate := buildQueue(config{DatabaseURL: "postgres://example", QueueBackend: "memory"}, jobs.NewMemoryStore(), testLogger())
	if _, ok := queue.(*jobs.MemoryQueue); !ok {
		t.Fatalf("expected memory queue, got %T", queue)
	}
	if !hydrate {
		t.Fatal("expected explicit memory queue to require startup hydration")
	}
}

func TestEnqueuePendingJobs(t *testing.T) {
	store := jobs.NewMemoryStore()
	queued, err := store.Create(jobs.CreateJobParams{Name: "queued", Type: "demo.queued"})
	if err != nil {
		t.Fatalf("create queued job: %v", err)
	}
	running, err := store.Create(jobs.CreateJobParams{Name: "running", Type: "demo.running"})
	if err != nil {
		t.Fatalf("create running job: %v", err)
	}
	if _, err := store.MarkRunning(running.ID); err != nil {
		t.Fatalf("mark running: %v", err)
	}
	succeeded, err := store.Create(jobs.CreateJobParams{Name: "succeeded", Type: "demo.succeeded"})
	if err != nil {
		t.Fatalf("create succeeded job: %v", err)
	}
	if _, err := store.MarkSucceeded(succeeded.ID, "done"); err != nil {
		t.Fatalf("mark succeeded: %v", err)
	}

	queue := jobs.NewMemoryQueue(2)
	if err := enqueuePendingJobs(context.Background(), store, queue); err != nil {
		t.Fatalf("enqueue pending jobs: %v", err)
	}

	if queue.Len() != 2 {
		t.Fatalf("expected 2 pending jobs to be enqueued, got %d", queue.Len())
	}

	got := map[string]bool{}
	for i := 0; i < 2; i++ {
		id, err := queue.Dequeue(context.Background())
		if err != nil {
			t.Fatalf("dequeue pending job: %v", err)
		}
		got[id] = true
	}

	if !got[queued.ID] {
		t.Fatalf("expected queued job %s to be requeued; got %#v", queued.ID, got)
	}
	if !got[running.ID] {
		t.Fatalf("expected running job %s to be requeued; got %#v", running.ID, got)
	}
	if got[succeeded.ID] {
		t.Fatalf("did not expect succeeded job %s to be requeued", succeeded.ID)
	}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
