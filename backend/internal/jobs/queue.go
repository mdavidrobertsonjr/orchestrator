package jobs

import (
	"context"
	"errors"
	"time"
)

var ErrQueueFull = errors.New("job queue is full")

type Queue interface {
	Enqueue(ctx context.Context, jobID string) error
	Dequeue(ctx context.Context) (string, error)
	Len() int
	Cap() int
}

type ClaimQueue interface {
	Queue
	Claim(ctx context.Context, workerID string, leaseUntil time.Time) (*Job, error)
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

type StoreQueue struct {
	store     Store
	pollDelay time.Duration
}

func NewStoreQueue(store Store, pollDelay time.Duration) *StoreQueue {
	if pollDelay <= 0 {
		pollDelay = 250 * time.Millisecond
	}
	return &StoreQueue{store: store, pollDelay: pollDelay}
}

func (q *StoreQueue) Enqueue(ctx context.Context, jobID string) error {
	_, err := q.store.Get(jobID)
	if err != nil {
		return err
	}
	return ctx.Err()
}

func (q *StoreQueue) Claim(ctx context.Context, workerID string, leaseUntil time.Time) (*Job, error) {
	for {
		job, err := q.store.ClaimQueued(workerID, leaseUntil)
		if err == nil {
			return job, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return nil, err
		}

		timer := time.NewTimer(q.pollDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (q *StoreQueue) Dequeue(ctx context.Context) (string, error) {
	for {
		jobs, err := q.store.List()
		if err != nil {
			return "", err
		}
		for i := len(jobs) - 1; i >= 0; i-- {
			if jobs[i].Status == StatusQueued {
				return jobs[i].ID, nil
			}
		}

		timer := time.NewTimer(q.pollDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", ctx.Err()
		case <-timer.C:
		}
	}
}

func (q *StoreQueue) Len() int {
	jobs, err := q.store.List()
	if err != nil {
		return 0
	}

	queued := 0
	for _, job := range jobs {
		if job.Status == StatusQueued {
			queued++
		}
	}
	return queued
}

func (q *StoreQueue) Cap() int {
	return 0
}
