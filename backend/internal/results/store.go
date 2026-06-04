package results

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sort"
	"sync"
	"time"
)

var ErrNotFound = errors.New("result not found")

type Store interface {
	Create(params CreateResultParams) (*Result, error)
	Get(id string) (*Result, error)
	List() ([]*Result, error)
	ListByJob(jobID string) ([]*Result, error)
	ListByWorkflow(workflowID string) ([]*Result, error)
}

type MemoryStore struct {
	mu      sync.RWMutex
	results map[string]*Result
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		results: make(map[string]*Result),
	}
}

func (s *MemoryStore) Create(params CreateResultParams) (*Result, error) {
	now := time.Now().UTC()
	result := &Result{
		ID:         newID(),
		JobID:      params.JobID,
		WorkflowID: params.WorkflowID,
		Type:       params.Type,
		Summary:    params.Summary,
		Data:       cloneMapAny(params.Data),
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.results[result.ID] = result
	return cloneResult(result), nil
}

func (s *MemoryStore) Get(id string) (*Result, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result, ok := s.results[id]
	if !ok {
		return nil, ErrNotFound
	}
	return cloneResult(result), nil
}

func (s *MemoryStore) List() ([]*Result, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]*Result, 0, len(s.results))
	for _, result := range s.results {
		out = append(out, cloneResult(result))
	}
	sortResults(out)
	return out, nil
}

func (s *MemoryStore) ListByJob(jobID string) ([]*Result, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var out []*Result
	for _, result := range s.results {
		if result.JobID == jobID {
			out = append(out, cloneResult(result))
		}
	}
	sortResults(out)
	return out, nil
}

func (s *MemoryStore) ListByWorkflow(workflowID string) ([]*Result, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var out []*Result
	for _, result := range s.results {
		if result.WorkflowID == workflowID {
			out = append(out, cloneResult(result))
		}
	}
	sortResults(out)
	return out, nil
}

func sortResults(results []*Result) {
	sort.Slice(results, func(i, j int) bool {
		return results[i].CreatedAt.After(results[j].CreatedAt)
	})
}

func cloneResult(result *Result) *Result {
	if result == nil {
		return nil
	}

	clone := *result
	clone.Data = cloneMapAny(result.Data)
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

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(b[:])
}
