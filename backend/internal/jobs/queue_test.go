package jobs

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMemoryQueueEnqueueDequeue(t *testing.T) {
	queue := NewMemoryQueue(2)

	if err := queue.Enqueue(context.Background(), "job-1"); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}
	if queue.Len() != 1 {
		t.Fatalf("expected queue length 1, got %d", queue.Len())
	}
	if queue.Cap() != 2 {
		t.Fatalf("expected queue capacity 2, got %d", queue.Cap())
	}

	id, err := queue.Dequeue(context.Background())
	if err != nil {
		t.Fatalf("dequeue job: %v", err)
	}
	if id != "job-1" {
		t.Fatalf("expected job-1, got %q", id)
	}
	if queue.Len() != 0 {
		t.Fatalf("expected empty queue, got length %d", queue.Len())
	}
}

func TestMemoryQueueFull(t *testing.T) {
	queue := NewMemoryQueue(1)

	if err := queue.Enqueue(context.Background(), "job-1"); err != nil {
		t.Fatalf("enqueue first job: %v", err)
	}
	if err := queue.Enqueue(context.Background(), "job-2"); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("expected ErrQueueFull, got %v", err)
	}
}

func TestMemoryQueueDequeueHonorsContext(t *testing.T) {
	queue := NewMemoryQueue(1)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := queue.Dequeue(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}
}

func TestStoreQueueDequeueUsesStoredQueuedJobs(t *testing.T) {
	store := NewMemoryStore()
	queue := NewStoreQueue(store, time.Millisecond)

	first, err := store.Create(CreateJobParams{Name: "first", Type: "demo.sleep"})
	if err != nil {
		t.Fatalf("create first job: %v", err)
	}
	second, err := store.Create(CreateJobParams{Name: "second", Type: "demo.sleep"})
	if err != nil {
		t.Fatalf("create second job: %v", err)
	}
	if _, err := store.MarkSucceeded(first.ID, "done"); err != nil {
		t.Fatalf("mark first succeeded: %v", err)
	}

	if err := queue.Enqueue(context.Background(), second.ID); err != nil {
		t.Fatalf("enqueue stored job: %v", err)
	}
	if queue.Len() != 1 {
		t.Fatalf("expected one queued job, got %d", queue.Len())
	}
	if queue.Cap() != 0 {
		t.Fatalf("expected unbounded store queue capacity marker, got %d", queue.Cap())
	}

	id, err := queue.Dequeue(context.Background())
	if err != nil {
		t.Fatalf("dequeue stored job: %v", err)
	}
	if id != second.ID {
		t.Fatalf("expected second job %q, got %q", second.ID, id)
	}
}

func TestStoreQueueDequeueHonorsContext(t *testing.T) {
	queue := NewStoreQueue(NewMemoryStore(), time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := queue.Dequeue(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}
}

func TestStoreQueueEnqueueMissingJob(t *testing.T) {
	queue := NewStoreQueue(NewMemoryStore(), time.Millisecond)

	if err := queue.Enqueue(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}
