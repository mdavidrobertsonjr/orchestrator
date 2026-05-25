package jobs

import (
	"context"
	"errors"
	"testing"
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
