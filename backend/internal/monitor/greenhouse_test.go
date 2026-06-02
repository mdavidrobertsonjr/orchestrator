package monitor

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestGreenhouseSourceFetchesAndNormalizesJobs(t *testing.T) {
	var requestedPath string
	var requestedQuery string

	source := NewGreenhouseSource(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			requestedPath = req.URL.Path
			requestedQuery = req.URL.RawQuery
			return jsonResponse(`{
			"jobs": [
				{
					"id": 123,
					"internal_job_id": 456,
					"title": "Software Engineer, New Grad",
					"absolute_url": "https://boards.greenhouse.io/acme/jobs/123",
					"location": {"name": "New York, NY"},
					"department": {"name": "Engineering"},
					"offices": [{"name": "New York", "location": "New York, NY, United States"}]
				}
			]
		}`), nil
		}),
	})

	candidates, err := source.Fetch(t.Context(), SourceConfig{
		Type:       "greenhouse",
		Company:    "Acme",
		BoardToken: "acme",
	})
	if err != nil {
		t.Fatalf("fetch greenhouse jobs: %v", err)
	}

	if requestedPath != "/v1/boards/acme/jobs" {
		t.Fatalf("unexpected request path %q", requestedPath)
	}
	if requestedQuery != "content=true" {
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
	if candidate.URL != "https://boards.greenhouse.io/acme/jobs/123" {
		t.Fatalf("unexpected URL %q", candidate.URL)
	}
	if candidate.Location != "New York, NY" {
		t.Fatalf("unexpected location %q", candidate.Location)
	}
	if candidate.Source != "greenhouse" || candidate.SourceID != "123" {
		t.Fatalf("unexpected source identity: %#v", candidate)
	}
	if candidate.Metadata["board_token"] != "acme" || candidate.Metadata["internal_job_id"] != "456" || candidate.Metadata["department"] != "Engineering" {
		t.Fatalf("unexpected metadata: %#v", candidate.Metadata)
	}
}

func TestGreenhouseSourceUsesOfficeLocationFallback(t *testing.T) {
	source := NewGreenhouseSource(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return jsonResponse(`{
			"jobs": [
				{
					"id": 789,
					"title": "Backend Engineer",
					"absolute_url": "https://boards.greenhouse.io/acme/jobs/789",
					"location": {"name": ""},
					"offices": [{"name": "NYC", "location": "New York, NY, United States"}]
				}
			]
		}`), nil
		}),
	})

	candidates, err := source.Fetch(t.Context(), SourceConfig{
		Type:       "greenhouse",
		Name:       "acme",
		BoardToken: "acme",
	})
	if err != nil {
		t.Fatalf("fetch greenhouse jobs: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected one candidate, got %d", len(candidates))
	}
	if candidates[0].Location != "New York, NY, United States" {
		t.Fatalf("expected office location fallback, got %q", candidates[0].Location)
	}
}

func TestGreenhouseSourceRequiresBoardToken(t *testing.T) {
	source := NewGreenhouseSource(nil)

	_, err := source.Fetch(t.Context(), SourceConfig{Type: "greenhouse"})
	if err == nil {
		t.Fatal("expected missing board token error")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func jsonResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
