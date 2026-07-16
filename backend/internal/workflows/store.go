package workflows

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sort"
	"sync"
	"time"
)

var ErrNotFound = errors.New("workflow not found")

type Store interface {
	Create(params CreateWorkflowParams) (*Workflow, error)
	Get(id string) (*Workflow, error)
	List() ([]*Workflow, error)
	ListDue(now time.Time) ([]*Workflow, error)
	MarkDispatched(id string, jobID string, lastRunAt time.Time, nextRunAt time.Time) (*Workflow, error)
	SetEnabled(id string, enabled bool) (*Workflow, error)
	Delete(id string) error
}

type MemoryStore struct {
	mu        sync.RWMutex
	workflows map[string]*Workflow
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		workflows: make(map[string]*Workflow),
	}
}

func (s *MemoryStore) Create(params CreateWorkflowParams) (*Workflow, error) {
	now := time.Now().UTC()
	maxAttempts := params.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	nextRunAt := defaultNextRunAt(now, params)

	workflow := &Workflow{
		ID:              newID(),
		Name:            params.Name,
		JobType:         params.JobType,
		Payload:         cloneMapAny(params.Payload),
		Metadata:        cloneMapString(params.Metadata),
		MaxAttempts:     maxAttempts,
		Enabled:         params.Enabled,
		IntervalSeconds: params.IntervalSeconds,
		NextRunAt:       nextRunAt,
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.workflows[workflow.ID] = workflow
	return cloneWorkflow(workflow), nil
}

func defaultNextRunAt(now time.Time, params CreateWorkflowParams) time.Time {
	if params.NextRunAt != nil {
		return params.NextRunAt.UTC()
	}
	if params.IntervalSeconds > 0 {
		return now.Add(time.Duration(params.IntervalSeconds) * time.Second)
	}
	return now
}

func (s *MemoryStore) Get(id string) (*Workflow, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	workflow, ok := s.workflows[id]
	if !ok {
		return nil, ErrNotFound
	}
	return cloneWorkflow(workflow), nil
}

func (s *MemoryStore) List() ([]*Workflow, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]*Workflow, 0, len(s.workflows))
	for _, workflow := range s.workflows {
		out = append(out, cloneWorkflow(workflow))
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})

	return out, nil
}

func (s *MemoryStore) ListDue(now time.Time) ([]*Workflow, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	now = now.UTC()
	var out []*Workflow
	for _, workflow := range s.workflows {
		if !workflow.Enabled || workflow.NextRunAt.After(now) {
			continue
		}
		out = append(out, cloneWorkflow(workflow))
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].NextRunAt.Before(out[j].NextRunAt)
	})

	return out, nil
}

func (s *MemoryStore) MarkDispatched(id string, jobID string, lastRunAt time.Time, nextRunAt time.Time) (*Workflow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	workflow, ok := s.workflows[id]
	if !ok {
		return nil, ErrNotFound
	}

	now := time.Now().UTC()
	lastRunAt = lastRunAt.UTC()
	workflow.LastRunAt = &lastRunAt
	workflow.LastJobID = jobID
	workflow.NextRunAt = nextRunAt.UTC()
	workflow.UpdatedAt = now
	return cloneWorkflow(workflow), nil
}

func (s *MemoryStore) SetEnabled(id string, enabled bool) (*Workflow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	workflow, ok := s.workflows[id]
	if !ok {
		return nil, ErrNotFound
	}

	workflow.Enabled = enabled
	workflow.UpdatedAt = time.Now().UTC()
	return cloneWorkflow(workflow), nil
}

func (s *MemoryStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.workflows[id]; !ok {
		return ErrNotFound
	}
	delete(s.workflows, id)
	return nil
}

func cloneWorkflow(workflow *Workflow) *Workflow {
	if workflow == nil {
		return nil
	}

	clone := *workflow
	clone.Payload = cloneMapAny(workflow.Payload)
	clone.Metadata = cloneMapString(workflow.Metadata)
	if workflow.LastRunAt != nil {
		lastRunAt := *workflow.LastRunAt
		clone.LastRunAt = &lastRunAt
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

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(b[:])
}
