package workers

import (
	"errors"
	"sort"
	"sync"
	"time"
)

type Status string

const (
	StatusIdle    Status = "idle"
	StatusRunning Status = "running"
	StatusStopped Status = "stopped"
)

var ErrNotFound = errors.New("worker not found")

type Worker struct {
	ID            string    `json:"id"`
	Status        Status    `json:"status"`
	CurrentJobID  string    `json:"current_job_id,omitempty"`
	LastHeartbeat time.Time `json:"last_heartbeat"`
	StartedAt     time.Time `json:"started_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type Registry interface {
	Register(id string) (*Worker, error)
	Heartbeat(id string) error
	MarkRunning(id string, jobID string) (*Worker, error)
	MarkIdle(id string) (*Worker, error)
	MarkStopped(id string) (*Worker, error)
	Get(id string) (*Worker, error)
	List() []*Worker
}

type MemoryRegistry struct {
	mu      sync.RWMutex
	workers map[string]*Worker
}

func NewMemoryRegistry() *MemoryRegistry {
	return &MemoryRegistry{
		workers: make(map[string]*Worker),
	}
}

func (r *MemoryRegistry) Register(id string) (*Worker, error) {
	now := time.Now().UTC()

	r.mu.Lock()
	defer r.mu.Unlock()

	worker, ok := r.workers[id]
	if !ok {
		worker = &Worker{
			ID:        id,
			StartedAt: now,
		}
		r.workers[id] = worker
	}

	worker.Status = StatusIdle
	worker.CurrentJobID = ""
	worker.LastHeartbeat = now
	worker.UpdatedAt = now

	return cloneWorker(worker), nil
}

func (r *MemoryRegistry) Heartbeat(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	worker, ok := r.workers[id]
	if !ok {
		return ErrNotFound
	}

	now := time.Now().UTC()
	worker.LastHeartbeat = now
	worker.UpdatedAt = now
	return nil
}

func (r *MemoryRegistry) MarkRunning(id string, jobID string) (*Worker, error) {
	return r.update(id, StatusRunning, jobID)
}

func (r *MemoryRegistry) MarkIdle(id string) (*Worker, error) {
	return r.update(id, StatusIdle, "")
}

func (r *MemoryRegistry) MarkStopped(id string) (*Worker, error) {
	return r.update(id, StatusStopped, "")
}

func (r *MemoryRegistry) Get(id string) (*Worker, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	worker, ok := r.workers[id]
	if !ok {
		return nil, ErrNotFound
	}
	return cloneWorker(worker), nil
}

func (r *MemoryRegistry) List() []*Worker {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]*Worker, 0, len(r.workers))
	for _, worker := range r.workers {
		out = append(out, cloneWorker(worker))
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].ID < out[j].ID
	})

	return out
}

func (r *MemoryRegistry) update(id string, status Status, currentJobID string) (*Worker, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	worker, ok := r.workers[id]
	if !ok {
		return nil, ErrNotFound
	}

	now := time.Now().UTC()
	worker.Status = status
	worker.CurrentJobID = currentJobID
	worker.LastHeartbeat = now
	worker.UpdatedAt = now

	return cloneWorker(worker), nil
}

func cloneWorker(worker *Worker) *Worker {
	if worker == nil {
		return nil
	}

	clone := *worker
	return &clone
}
