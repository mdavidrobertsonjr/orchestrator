package monitor

import (
	"testing"

	"orchestrator/backend/internal/postings"
)

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

func containsLog(logs []string, expected string) bool {
	for _, log := range logs {
		if log == expected {
			return true
		}
	}
	return false
}
