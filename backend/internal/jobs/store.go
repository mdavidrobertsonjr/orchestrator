package jobs

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sort"
	"sync"
	"time"
)

var ErrNotFound = errors.New("job not found")

type Store interface {
	Create(params CreateJobParams) (*Job, error)
	Get(id string) (*Job, error)
	List() ([]*Job, error)
	MarkRunning(id string) (*Job, error)
	MarkSucceeded(id string, message string) (*Job, error)
	MarkFailed(id string, errMessage string) (*Job, error)
	AppendLog(id string, message string) error
}

type MemoryStore struct {
	mu   sync.RWMutex
	jobs map[string]*Job
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		jobs: make(map[string]*Job),
	}
}

func (s *MemoryStore) Create(params CreateJobParams) (*Job, error) {
	now := time.Now().UTC()
	maxAttempts := params.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 1
	}

	job := &Job{
		ID:          newID(),
		Name:        params.Name,
		Type:        params.Type,
		Status:      StatusQueued,
		Payload:     cloneMapAny(params.Payload),
		MaxAttempts: maxAttempts,
		Metadata:    cloneMapString(params.Metadata),
		CreatedAt:   now,
		UpdatedAt:   now,
		Logs: []LogEntry{{
			Time:    now,
			Message: "job queued",
		}},
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs[job.ID] = job

	return cloneJob(job), nil
}

func (s *MemoryStore) Get(id string) (*Job, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	job, ok := s.jobs[id]
	if !ok {
		return nil, ErrNotFound
	}
	return cloneJob(job), nil
}

func (s *MemoryStore) List() ([]*Job, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]*Job, 0, len(s.jobs))
	for _, job := range s.jobs {
		out = append(out, cloneJob(job))
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})

	return out, nil
}

func (s *MemoryStore) MarkRunning(id string) (*Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	job, ok := s.jobs[id]
	if !ok {
		return nil, ErrNotFound
	}

	now := time.Now().UTC()
	job.Status = StatusRunning
	job.Attempts++
	job.UpdatedAt = now
	job.StartedAt = &now
	job.FinishedAt = nil
	job.Error = ""
	job.Logs = append(job.Logs, LogEntry{Time: now, Message: "job started"})

	return cloneJob(job), nil
}

func (s *MemoryStore) MarkSucceeded(id string, message string) (*Job, error) {
	return s.finish(id, StatusSucceeded, "", message)
}

func (s *MemoryStore) MarkFailed(id string, errMessage string) (*Job, error) {
	return s.finish(id, StatusFailed, errMessage, "job failed: "+errMessage)
}

func (s *MemoryStore) AppendLog(id string, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	job, ok := s.jobs[id]
	if !ok {
		return ErrNotFound
	}

	now := time.Now().UTC()
	job.UpdatedAt = now
	job.Logs = append(job.Logs, LogEntry{Time: now, Message: message})
	return nil
}

func (s *MemoryStore) finish(id string, status Status, errMessage string, logMessage string) (*Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	job, ok := s.jobs[id]
	if !ok {
		return nil, ErrNotFound
	}

	now := time.Now().UTC()
	job.Status = status
	job.Error = errMessage
	job.UpdatedAt = now
	job.FinishedAt = &now
	job.Logs = append(job.Logs, LogEntry{Time: now, Message: logMessage})

	return cloneJob(job), nil
}

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(b[:])
}

func cloneJob(job *Job) *Job {
	if job == nil {
		return nil
	}

	clone := *job
	clone.Payload = cloneMapAny(job.Payload)
	clone.Metadata = cloneMapString(job.Metadata)
	clone.Logs = append([]LogEntry(nil), job.Logs...)

	if job.StartedAt != nil {
		started := *job.StartedAt
		clone.StartedAt = &started
	}
	if job.FinishedAt != nil {
		finished := *job.FinishedAt
		clone.FinishedAt = &finished
	}

	return &clone
}

func cloneMapAny(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}

	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func cloneMapString(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}

	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
