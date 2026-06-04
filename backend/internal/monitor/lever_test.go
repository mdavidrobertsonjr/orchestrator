package monitor

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestLeverSourceFetchesAndNormalizesPostings(t *testing.T) {
	var requestedPath string
	var requestedQuery string
	source := NewLeverSource(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			requestedPath = req.URL.Path
			requestedQuery = req.URL.RawQuery
			return leverJSONResponse(`[
				{
					"id": "posting-1",
					"text": "Software Engineer, New Grad",
					"hostedUrl": "https://jobs.lever.co/acme/posting-1",
					"applyUrl": "https://jobs.lever.co/acme/posting-1/apply",
					"categories": {
						"location": "New York, NY",
						"team": "Engineering",
						"department": "Product Engineering",
						"commitment": "Full-time"
					},
					"createdAt": 1780502400000
				}
			]`), nil
		}),
	})
	source.baseURL = "https://lever.test"

	candidates, err := source.Fetch(t.Context(), SourceConfig{
		Type:        "lever",
		Company:     "Acme",
		AccountName: "acme",
	})
	if err != nil {
		t.Fatalf("fetch lever postings: %v", err)
	}

	if requestedPath != "/v0/postings/acme" {
		t.Fatalf("unexpected request path %q", requestedPath)
	}
	if requestedQuery != "mode=json" {
		t.Fatalf("unexpected query %q", requestedQuery)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected one candidate, got %d", len(candidates))
	}

	candidate := candidates[0]
	if candidate.Company != "Acme" {
		t.Fatalf("expected company Acme, got %q", candidate.Company)
	}
	if candidate.Title != "Software Engineer, New Grad" {
		t.Fatalf("unexpected title %q", candidate.Title)
	}
	if candidate.URL != "https://jobs.lever.co/acme/posting-1" {
		t.Fatalf("unexpected URL %q", candidate.URL)
	}
	if candidate.Location != "New York, NY" {
		t.Fatalf("unexpected location %q", candidate.Location)
	}
	if candidate.Source != "lever" || candidate.SourceID != "posting-1" {
		t.Fatalf("unexpected source identity: %#v", candidate)
	}
	if candidate.PostedAt == nil || !candidate.PostedAt.Equal(time.Date(2026, 6, 3, 16, 0, 0, 0, time.UTC)) {
		t.Fatalf("unexpected posted timestamp: %#v", candidate.PostedAt)
	}
	if candidate.Metadata["account_name"] != "acme" || candidate.Metadata["team"] != "Engineering" || candidate.Metadata["commitment"] != "Full-time" {
		t.Fatalf("unexpected metadata: %#v", candidate.Metadata)
	}
}

func TestLeverSourceUsesApplyURLFallback(t *testing.T) {
	source := NewLeverSource(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return leverJSONResponse(`[
				{
					"id": "posting-2",
					"text": "Backend Engineer",
					"applyUrl": "https://jobs.lever.co/acme/posting-2/apply",
					"categories": {"location": "Remote"}
				}
			]`), nil
		}),
	})
	source.baseURL = "https://lever.test"

	candidates, err := source.Fetch(t.Context(), SourceConfig{
		Type:        "lever",
		AccountName: "acme",
	})
	if err != nil {
		t.Fatalf("fetch lever postings: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected one candidate, got %d", len(candidates))
	}
	if candidates[0].URL != "https://jobs.lever.co/acme/posting-2/apply" {
		t.Fatalf("expected apply URL fallback, got %q", candidates[0].URL)
	}
}

func TestLeverSourceRequiresAccountName(t *testing.T) {
	source := NewLeverSource(nil)

	_, err := source.Fetch(t.Context(), SourceConfig{Type: "lever"})
	if err == nil {
		t.Fatal("expected missing account name error")
	}
}

func leverJSONResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
