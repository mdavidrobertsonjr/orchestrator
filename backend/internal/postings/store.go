package postings

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

var ErrNotFound = errors.New("posting not found")

type Store interface {
	Upsert(params UpsertPostingParams) (*Posting, bool, error)
	Get(id string) (*Posting, error)
	SetApplied(id string, applied bool) (*Posting, error)
	Delete(id string) error
	List() ([]*Posting, error)
}

type ListParams struct {
	Company    string
	Source     string
	Location   string
	Query      string
	MinScore   int
	Applied    string
	Freshness  string
	FreshAfter time.Time
	Limit      int
	Offset     int
}

type MemoryStore struct {
	mu       sync.RWMutex
	postings map[string]*Posting
	byDedupe map[string]string
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		postings: make(map[string]*Posting),
		byDedupe: make(map[string]string),
	}
}

func (s *MemoryStore) Upsert(params UpsertPostingParams) (*Posting, bool, error) {
	now := time.Now().UTC()
	dedupeKey := dedupeKey(params)

	s.mu.Lock()
	defer s.mu.Unlock()

	if id, ok := s.byDedupe[dedupeKey]; ok {
		posting := s.postings[id]
		posting.Company = strings.TrimSpace(params.Company)
		posting.Title = strings.TrimSpace(params.Title)
		posting.URL = strings.TrimSpace(params.URL)
		posting.Location = strings.TrimSpace(params.Location)
		posting.Source = strings.TrimSpace(params.Source)
		posting.SourceID = strings.TrimSpace(params.SourceID)
		posting.PostedAt = cloneTime(params.PostedAt)
		posting.LastSeenAt = now
		posting.MatchedAt = cloneTime(params.MatchedAt)
		posting.MatchScore = params.MatchScore
		posting.MatchReasons = append([]string(nil), params.MatchReasons...)
		posting.Metadata = cloneMapString(params.Metadata)
		return clonePosting(posting), false, nil
	}

	posting := &Posting{
		ID:           newID(),
		Company:      strings.TrimSpace(params.Company),
		Title:        strings.TrimSpace(params.Title),
		URL:          strings.TrimSpace(params.URL),
		Location:     strings.TrimSpace(params.Location),
		Source:       strings.TrimSpace(params.Source),
		SourceID:     strings.TrimSpace(params.SourceID),
		DedupeKey:    dedupeKey,
		PostedAt:     cloneTime(params.PostedAt),
		FirstSeenAt:  now,
		LastSeenAt:   now,
		MatchedAt:    cloneTime(params.MatchedAt),
		MatchScore:   params.MatchScore,
		MatchReasons: append([]string(nil), params.MatchReasons...),
		Metadata:     cloneMapString(params.Metadata),
	}

	s.postings[posting.ID] = posting
	s.byDedupe[posting.DedupeKey] = posting.ID
	return clonePosting(posting), true, nil
}

func (s *MemoryStore) Get(id string) (*Posting, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	posting, ok := s.postings[id]
	if !ok {
		return nil, ErrNotFound
	}
	return clonePosting(posting), nil
}

func (s *MemoryStore) SetApplied(id string, applied bool) (*Posting, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	posting, ok := s.postings[id]
	if !ok {
		return nil, ErrNotFound
	}
	if applied {
		now := time.Now().UTC()
		posting.AppliedAt = &now
	} else {
		posting.AppliedAt = nil
	}
	return clonePosting(posting), nil
}

func (s *MemoryStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	posting, ok := s.postings[id]
	if !ok || posting.DismissedAt != nil {
		return ErrNotFound
	}
	now := time.Now().UTC()
	posting.DismissedAt = &now
	return nil
}

func (s *MemoryStore) List() ([]*Posting, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]*Posting, 0, len(s.postings))
	for _, posting := range s.postings {
		if posting.DismissedAt != nil {
			continue
		}
		out = append(out, clonePosting(posting))
	}

	sort.Slice(out, func(i, j int) bool {
		return lessPosting(out[i], out[j])
	})

	return out, nil
}

func lessPosting(a, b *Posting) bool {
	if a.MatchScore != b.MatchScore {
		return a.MatchScore > b.MatchScore
	}
	return a.FirstSeenAt.After(b.FirstSeenAt)
}

func dedupeKey(params UpsertPostingParams) string {
	source := strings.ToLower(strings.TrimSpace(params.Source))
	sourceID := strings.ToLower(strings.TrimSpace(params.SourceID))
	if source != "" && sourceID != "" {
		return source + ":" + sourceID
	}

	normalizedURL := normalizeURL(params.URL)
	if normalizedURL != "" {
		return source + ":url:" + normalizedURL
	}

	return source + ":fallback:" + strings.ToLower(strings.TrimSpace(params.Company)) + ":" + strings.ToLower(strings.TrimSpace(params.Title)) + ":" + strings.ToLower(strings.TrimSpace(params.Location))
}

func normalizeURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return strings.ToLower(raw)
	}

	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Fragment = ""

	query := parsed.Query()
	for key := range query {
		if strings.HasPrefix(strings.ToLower(key), "utm_") {
			query.Del(key)
		}
	}
	parsed.RawQuery = query.Encode()

	return strings.TrimRight(parsed.String(), "/")
}

func clonePosting(posting *Posting) *Posting {
	if posting == nil {
		return nil
	}

	clone := *posting
	clone.PostedAt = cloneTime(posting.PostedAt)
	clone.MatchedAt = cloneTime(posting.MatchedAt)
	clone.AppliedAt = cloneTime(posting.AppliedAt)
	clone.DismissedAt = cloneTime(posting.DismissedAt)
	clone.MatchReasons = append([]string(nil), posting.MatchReasons...)
	clone.Metadata = cloneMapString(posting.Metadata)
	return &clone
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	clone := *value
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
