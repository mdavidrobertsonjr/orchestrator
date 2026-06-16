package notifications

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sort"
	"sync"
	"time"
)

var ErrNotFound = errors.New("notification delivery not found")

type Store interface {
	Create(params CreateDeliveryParams) (*Delivery, error)
	MarkAttempt(id string) (*Delivery, error)
	MarkSucceeded(id string) (*Delivery, error)
	MarkFailed(id string, message string) (*Delivery, error)
	List() ([]*Delivery, error)
}

type MemoryStore struct {
	mu         sync.RWMutex
	deliveries map[string]*Delivery
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{deliveries: map[string]*Delivery{}}
}

func (s *MemoryStore) Create(params CreateDeliveryParams) (*Delivery, error) {
	now := time.Now().UTC()
	delivery := &Delivery{
		ID:         newID(),
		JobID:      params.JobID,
		WorkflowID: params.WorkflowID,
		Kind:       params.Kind,
		Provider:   params.Provider,
		Status:     "pending",
		Recipients: append([]string(nil), params.Recipients...),
		Subject:    params.Subject,
		Metadata:   cloneMapAny(params.Metadata),
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deliveries[delivery.ID] = delivery
	return cloneDelivery(delivery), nil
}

func (s *MemoryStore) MarkAttempt(id string) (*Delivery, error) {
	return s.update(id, func(delivery *Delivery, now time.Time) {
		delivery.Status = "sending"
		delivery.Attempts++
		delivery.Error = ""
		delivery.LastAttempt = &now
	})
}

func (s *MemoryStore) MarkSucceeded(id string) (*Delivery, error) {
	return s.update(id, func(delivery *Delivery, now time.Time) {
		delivery.Status = "succeeded"
		delivery.Error = ""
	})
}

func (s *MemoryStore) MarkFailed(id string, message string) (*Delivery, error) {
	return s.update(id, func(delivery *Delivery, now time.Time) {
		delivery.Status = "failed"
		delivery.Error = message
	})
}

func (s *MemoryStore) update(id string, update func(*Delivery, time.Time)) (*Delivery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delivery, ok := s.deliveries[id]
	if !ok {
		return nil, ErrNotFound
	}
	now := time.Now().UTC()
	update(delivery, now)
	delivery.UpdatedAt = now
	return cloneDelivery(delivery), nil
}

func (s *MemoryStore) List() ([]*Delivery, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Delivery, 0, len(s.deliveries))
	for _, delivery := range s.deliveries {
		out = append(out, cloneDelivery(delivery))
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

func cloneDelivery(in *Delivery) *Delivery {
	if in == nil {
		return nil
	}
	out := *in
	out.Recipients = append([]string(nil), in.Recipients...)
	out.Metadata = cloneMapAny(in.Metadata)
	if in.LastAttempt != nil {
		lastAttempt := *in.LastAttempt
		out.LastAttempt = &lastAttempt
	}
	return &out
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
