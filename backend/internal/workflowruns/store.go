package workflowruns

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sort"
	"sync"
	"time"
)

var ErrNotFound = errors.New("workflow run not found")

type Store interface {
	Create(params CreateRunParams) (*Run, error)
	Get(id string) (*Run, error)
	List() ([]*Run, error)
	ListByWorkflow(workflowID string) ([]*Run, error)
}

type MemoryStore struct {
	mu   sync.RWMutex
	runs map[string]*Run
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{runs: make(map[string]*Run)}
}

func (s *MemoryStore) Create(params CreateRunParams) (*Run, error) {
	now := time.Now().UTC()
	status := params.Status
	if status == "" {
		status = "queued"
	}
	run := &Run{
		ID:         newID(),
		WorkflowID: params.WorkflowID,
		JobID:      params.JobID,
		Trigger:    params.Trigger,
		Status:     status,
		Metadata:   cloneMapString(params.Metadata),
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs[run.ID] = run
	return cloneRun(run), nil
}

func (s *MemoryStore) Get(id string) (*Run, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	run, ok := s.runs[id]
	if !ok {
		return nil, ErrNotFound
	}
	return cloneRun(run), nil
}

func (s *MemoryStore) List() ([]*Run, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]*Run, 0, len(s.runs))
	for _, run := range s.runs {
		out = append(out, cloneRun(run))
	}
	sortRuns(out)
	return out, nil
}

func (s *MemoryStore) ListByWorkflow(workflowID string) ([]*Run, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var out []*Run
	for _, run := range s.runs {
		if run.WorkflowID == workflowID {
			out = append(out, cloneRun(run))
		}
	}
	sortRuns(out)
	return out, nil
}

func sortRuns(runs []*Run) {
	sort.Slice(runs, func(i, j int) bool {
		return runs[i].CreatedAt.After(runs[j].CreatedAt)
	})
}

func cloneRun(run *Run) *Run {
	if run == nil {
		return nil
	}
	clone := *run
	clone.Metadata = cloneMapString(run.Metadata)
	return &clone
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
