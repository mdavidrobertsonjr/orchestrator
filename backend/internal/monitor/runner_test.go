package monitor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"orchestrator/backend/internal/postings"
)

type failingSource struct{ err error }

func (s failingSource) Fetch(context.Context, SourceConfig) ([]Candidate, error) {
	return nil, s.err
}

type gateSource struct {
	started chan<- struct{}
	release <-chan struct{}
}

type countingSource struct{ calls *int }

func (s countingSource) Fetch(context.Context, SourceConfig) ([]Candidate, error) {
	*s.calls = *s.calls + 1
	return nil, nil
}

func TestRunnerReusesRecentlySuccessfulSourceOnRetry(t *testing.T) {
	calls := 0
	runner := NewRunner(postings.NewMemoryStore(), map[string]Source{"counted": countingSource{calls: &calls}})
	payload := map[string]any{"sources": []map[string]any{{"type": "counted", "name": "counted"}}}
	if _, err := runner.Run(t.Context(), payload, nil); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if _, err := runner.Run(t.Context(), payload, nil); err != nil {
		t.Fatalf("retry run: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected cached source to avoid refetch, got %d calls", calls)
	}
}

func TestRunnerBoundsSourceCache(t *testing.T) {
	runner := NewRunner(postings.NewMemoryStore(), nil)
	for index := 0; index < sourceCacheMaxEntries+1; index++ {
		runner.cacheSource("fake", SourceConfig{Name: fmt.Sprintf("source-%d", index)}, []Candidate{{Title: "job"}})
	}

	runner.cacheMu.Lock()
	defer runner.cacheMu.Unlock()
	if len(runner.sourceCache) != sourceCacheMaxEntries {
		t.Fatalf("expected cache size %d, got %d", sourceCacheMaxEntries, len(runner.sourceCache))
	}
}

func TestRetryDelayHonorsRetryAfter(t *testing.T) {
	response := &http.Response{Header: http.Header{"Retry-After": []string{"3"}}}
	if got := retryDelay(response, time.Second, 0); got != 3*time.Second {
		t.Fatalf("expected Retry-After delay, got %s", got)
	}
}

func (s gateSource) Fetch(context.Context, SourceConfig) ([]Candidate, error) {
	s.started <- struct{}{}
	<-s.release
	return nil, nil
}

func TestRunnerFetchesSourcesConcurrently(t *testing.T) {
	startedA := make(chan struct{}, 1)
	startedB := make(chan struct{}, 1)
	release := make(chan struct{})
	runner := NewRunner(postings.NewMemoryStore(), map[string]Source{
		"a": gateSource{started: startedA, release: release},
		"b": gateSource{started: startedB, release: release},
	})

	done := make(chan error, 1)
	go func() {
		_, err := runner.Run(t.Context(), map[string]any{"sources": []map[string]any{{"type": "a"}, {"type": "b"}}}, nil)
		done <- err
	}()

	for _, started := range []<-chan struct{}{startedA, startedB} {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("source did not start concurrently")
		}
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("run monitor: %v", err)
	}
}

func TestRunnerKeepsSuccessfulSourcesWhenOneFails(t *testing.T) {
	store := postings.NewMemoryStore()
	runner := NewRunner(store, map[string]Source{"broken": failingSource{err: errors.New("upstream unavailable")}})
	result, err := runner.Run(t.Context(), map[string]any{
		"sources": []map[string]any{
			{"type": "broken", "name": "Broken source"},
			{"type": "fake", "name": "Working source", "postings": []map[string]any{{
				"company": "Acme", "title": "Software Engineer", "url": "https://example.com/acme", "source_id": "acme-1",
			}}},
		},
		"min_score": float64(1),
	}, nil)
	if err != nil {
		t.Fatalf("expected partial source failure to succeed, got %v", err)
	}
	if len(result.SourceErrors) != 1 || result.Created != 1 {
		t.Fatalf("unexpected partial result: %#v", result)
	}
}

func TestSmartRecruitersURLSourceAlias(t *testing.T) {
	if got := smartRecruitersIdentifier("https://careers.smartrecruiters.com/Visa"); got != "Visa" {
		t.Fatalf("expected Visa identifier, got %q", got)
	}
	if got := smartRecruitersIdentifier("https://example.com/jobs.json"); got != "" {
		t.Fatalf("expected non-SmartRecruiters URL to have no identifier, got %q", got)
	}
}

func TestRunnerStoresNewMatchingPostings(t *testing.T) {
	store := postings.NewMemoryStore()
	runner := NewRunner(store, nil)

	var logs []string
	result, err := runner.Run(t.Context(), map[string]any{
		"sources": []map[string]any{
			{
				"type":    "fake",
				"name":    "fixture",
				"company": "Datadog",
				"postings": []map[string]any{
					{
						"company":   "Datadog",
						"title":     "Software Engineer, New Grad",
						"url":       "https://example.com/datadog/new-grad",
						"location":  "New York, NY",
						"source":    "greenhouse",
						"source_id": "dd-1",
					},
					{
						"company":   "Datadog",
						"title":     "Senior Software Engineer",
						"url":       "https://example.com/datadog/senior",
						"location":  "New York, NY",
						"source":    "greenhouse",
						"source_id": "dd-2",
					},
				},
			},
		},
		"keywords":          []string{"new grad"},
		"excluded_keywords": []string{"senior"},
		"locations":         []string{"new york"},
		"min_score":         float64(20),
	}, func(message string) {
		logs = append(logs, message)
	})
	if err != nil {
		t.Fatalf("run monitor: %v", err)
	}
	if result.Scanned != 2 || result.Matched != 1 || result.Created != 1 || result.Updated != 0 {
		t.Fatalf("unexpected result: %#v", result)
	}

	stored, err := store.List()
	if err != nil {
		t.Fatalf("list postings: %v", err)
	}
	if len(stored) != 1 {
		t.Fatalf("expected one stored posting, got %d", len(stored))
	}
	if stored[0].Title != "Software Engineer, New Grad" {
		t.Fatalf("unexpected stored posting: %#v", stored[0])
	}
	if stored[0].MatchScore != 40 {
		t.Fatalf("expected match score 40, got %d", stored[0].MatchScore)
	}
	if !containsLog(logs, "new matching posting: Datadog - Software Engineer, New Grad (https://example.com/datadog/new-grad)") {
		t.Fatalf("expected new posting log, got %#v", logs)
	}
}

func TestRunnerLimitsCandidatesPerSource(t *testing.T) {
	runner := NewRunner(postings.NewMemoryStore(), nil)
	result, err := runner.Run(t.Context(), map[string]any{
		"sources": []map[string]any{{
			"type": "fake", "name": "fixture", "limit": 1,
			"postings": []map[string]any{
				{"company": "Acme", "title": "One", "url": "https://example.com/1", "source_id": "1"},
				{"company": "Acme", "title": "Two", "url": "https://example.com/2", "source_id": "2"},
			},
		}},
		"min_score": float64(1),
	}, nil)
	if err != nil {
		t.Fatalf("run monitor: %v", err)
	}
	if result.Scanned != 1 || result.Created != 1 {
		t.Fatalf("expected one limited candidate, got %#v", result)
	}
}

func TestRunnerDedupesRepeatedMatches(t *testing.T) {
	store := postings.NewMemoryStore()
	runner := NewRunner(store, nil)
	payload := map[string]any{
		"sources": []map[string]any{
			{
				"type": "fake",
				"name": "fixture",
				"postings": []map[string]any{
					{
						"company":   "Ramp",
						"title":     "Software Engineer, New Grad",
						"url":       "https://example.com/ramp/new-grad",
						"location":  "NYC",
						"source":    "lever",
						"source_id": "ramp-1",
					},
				},
			},
		},
		"keywords":  []string{"new grad"},
		"min_score": float64(1),
	}

	first, err := runner.Run(t.Context(), payload, nil)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	second, err := runner.Run(t.Context(), payload, nil)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}

	if first.Created != 1 || first.Updated != 0 {
		t.Fatalf("unexpected first result: %#v", first)
	}
	if second.Created != 0 || second.Updated != 1 {
		t.Fatalf("unexpected second result: %#v", second)
	}
}

func TestRunnerRejectsLocationOnlyMatchesWhenKeywordsConfigured(t *testing.T) {
	store := postings.NewMemoryStore()
	runner := NewRunner(store, nil)

	result, err := runner.Run(t.Context(), map[string]any{
		"sources": []map[string]any{
			{
				"type": "fake",
				"name": "fixture",
				"postings": []map[string]any{
					{
						"company":   "Stripe",
						"title":     "Account Executive",
						"url":       "https://example.com/stripe/account-executive",
						"location":  "New York, NY",
						"source":    "greenhouse",
						"source_id": "stripe-sales-1",
					},
				},
			},
		},
		"keywords":  []string{"new grad", "software engineer"},
		"locations": []string{"new york"},
		"min_score": float64(20),
	}, nil)
	if err != nil {
		t.Fatalf("run monitor: %v", err)
	}
	if result.Scanned != 1 || result.Matched != 0 || result.Created != 0 || result.Updated != 0 {
		t.Fatalf("unexpected result: %#v", result)
	}

	stored, err := store.List()
	if err != nil {
		t.Fatalf("list postings: %v", err)
	}
	if len(stored) != 0 {
		t.Fatalf("expected no stored postings, got %d", len(stored))
	}
}

func TestRunnerRequiresKeywordConceptsForSeededMonitor(t *testing.T) {
	store := postings.NewMemoryStore()
	runner := NewRunner(store, nil)

	result, err := runner.Run(t.Context(), map[string]any{
		"sources": []map[string]any{
			{
				"type": "fake",
				"name": "fixture",
				"postings": []map[string]any{
					{
						"company":   "Scale AI",
						"title":     "Software Engineer, Platform",
						"url":       "https://example.com/scale/platform",
						"location":  "New York, NY",
						"source":    "greenhouse",
						"source_id": "scale-1",
					},
					{
						"company":   "Scale AI",
						"title":     "Early Career Software Engineer",
						"url":       "https://example.com/scale/early-career",
						"location":  "New York, NY",
						"source":    "greenhouse",
						"source_id": "scale-2",
					},
				},
			},
		},
		"keywords":          []string{"new grad", "early career", "university", "software engineer", "software engineering"},
		"excluded_keywords": []string{"senior", "staff", "principal"},
		"locations":         []string{"new york"},
		"min_score":         float64(20),
	}, nil)
	if err != nil {
		t.Fatalf("run monitor: %v", err)
	}
	if result.Scanned != 2 || result.Matched != 1 || result.Created != 1 || result.Updated != 0 {
		t.Fatalf("unexpected result: %#v", result)
	}

	stored, err := store.List()
	if err != nil {
		t.Fatalf("list postings: %v", err)
	}
	if len(stored) != 1 || stored[0].Title != "Early Career Software Engineer" {
		t.Fatalf("expected only early-career software posting, got %#v", stored)
	}
}

func TestRunnerCountsOnlyOneLocationMatch(t *testing.T) {
	store := postings.NewMemoryStore()
	runner := NewRunner(store, nil)

	result, err := runner.Run(t.Context(), map[string]any{
		"sources": []map[string]any{
			{
				"type": "fake",
				"name": "fixture",
				"postings": []map[string]any{
					{
						"company":   "Anduril",
						"title":     "Early Career Software Engineer",
						"url":       "https://example.com/anduril/early-career",
						"location":  "Costa Mesa, California; Seattle, Washington",
						"source":    "greenhouse",
						"source_id": "anduril-1",
					},
				},
			},
		},
		"keywords":  []string{"early career", "software engineer"},
		"locations": []string{"costa mesa", "seattle"},
		"min_score": float64(20),
	}, nil)
	if err != nil {
		t.Fatalf("run monitor: %v", err)
	}
	if result.Matched != 1 || result.Created != 1 {
		t.Fatalf("unexpected result: %#v", result)
	}

	stored, err := store.List()
	if err != nil {
		t.Fatalf("list postings: %v", err)
	}
	if len(stored) != 1 {
		t.Fatalf("expected one posting, got %#v", stored)
	}
	if stored[0].MatchScore != 60 {
		t.Fatalf("expected one location bonus for total score 60, got %d with reasons %#v", stored[0].MatchScore, stored[0].MatchReasons)
	}
	if countReasonPrefix(stored[0].MatchReasons, "location:") != 1 {
		t.Fatalf("expected one location reason, got %#v", stored[0].MatchReasons)
	}
}

func containsLog(logs []string, expected string) bool {
	for _, log := range logs {
		if log == expected {
			return true
		}
	}
	return false
}

func countReasonPrefix(reasons []string, prefix string) int {
	count := 0
	for _, reason := range reasons {
		if len(reason) >= len(prefix) && reason[:len(prefix)] == prefix {
			count++
		}
	}
	return count
}
