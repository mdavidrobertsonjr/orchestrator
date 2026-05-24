package jobs

import (
	"context"
	"errors"
)

var ErrQueueFull = errors.New("job queue is full")

type Queue interface {
	Enqueue(ctx context.Context, jobID string) error
	Dequeue(ctx context.Context) (string, error)
	Len() int
	Cap() int
}

type MemoryQueue struct {
	ch chan string
}

func NewMemoryQueue(size int) *MemoryQueue {
	if size <= 0 {
		size = 128
	}
	return &MemoryQueue{
		ch: make(chan string, size),
	}
}

func (q *MemoryQueue) Enqueue(ctx context.Context, jobID string) error {
	select {
	case q.ch <- jobID:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	default:
		return ErrQueueFull
	}
}

func (q *MemoryQueue) Dequeue(ctx context.Context) (string, error) {
	select {
	case id := <-q.ch:
		return id, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (q *MemoryQueue) Len() int {
	return len(q.ch)
}

func (q *MemoryQueue) Cap() int {
	return cap(q.ch)
}
