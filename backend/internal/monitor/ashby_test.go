package monitor

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestAshbySourceFetchesAndNormalizesJobs(t *testing.T) {
	var requestedPath string
	var requestedQuery string
	source := NewAshbySource(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			requestedPath = req.URL.Path
			requestedQuery = req.URL.RawQuery
			return ashbyJSONResponse(`{
				"apiVersion": "1",
				"jobs": [
					{
						"id": "job-1",
						"title": "Software Engineer, New Grad",
						"location": "New York, NY",
						"department": "Engineering",
						"team": "Product",
						"isListed": true,
						"isRemote": false,
						"workplaceType": "Hybrid",
						"publishedAt": "2026-06-03T16:21:55.393Z",
						"employmentType": "FullTime",
						"jobUrl": "https://jobs.ashbyhq.com/acme/job-1",
						"applyUrl": "https://jobs.ashbyhq.com/acme/job-1/application",
						"compensation": {
							"compensationTierSummary": "$120K - $150K"
						}
					},
					{
						"id": "unlisted",
						"title": "Hidden Role",
						"isListed": false
					}
				]
			}`), nil
		}),
	})
	source.baseURL = "https://ashby.test"

	candidates, err := source.Fetch(t.Context(), SourceConfig{
		Type:         "ashby",
		Company:      "Acme",
		JobBoardName: "acme",
	})
	if err != nil {
		t.Fatalf("fetch ashby jobs: %v", err)
	}

	if requestedPath != "/posting-api/job-board/acme" {
		t.Fatalf("unexpected request path %q", requestedPath)
	}
	if requestedQuery != "includeCompensation=true" {
		t.Fatalf("unexpected query %q", requestedQuery)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected one listed candidate, got %d", len(candidates))
	}

	candidate := candidates[0]
	if candidate.Company != "Acme" {
		t.Fatalf("expected company Acme, got %q", candidate.Company)
	}
	if candidate.Title != "Software Engineer, New Grad" {
		t.Fatalf("unexpected title %q", candidate.Title)
	}
	if candidate.URL != "https://jobs.ashbyhq.com/acme/job-1" {
		t.Fatalf("unexpected URL %q", candidate.URL)
	}
	if candidate.Location != "New York, NY" {
		t.Fatalf("unexpected location %q", candidate.Location)
	}
	if candidate.Source != "ashby" || candidate.SourceID != "job-1" {
		t.Fatalf("unexpected source identity: %#v", candidate)
	}
	if candidate.PostedAt == nil || !candidate.PostedAt.Equal(time.Date(2026, 6, 3, 16, 21, 55, 393000000, time.UTC)) {
		t.Fatalf("unexpected published timestamp: %#v", candidate.PostedAt)
	}
	if candidate.Metadata["job_board_name"] != "acme" || candidate.Metadata["team"] != "Product" || candidate.Metadata["compensation"] != "$120K - $150K" {
		t.Fatalf("unexpected metadata: %#v", candidate.Metadata)
	}
}

func TestAshbySourceUsesRemoteLocationFallbackAndApplyURL(t *testing.T) {
	source := NewAshbySource(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return ashbyJSONResponse(`{
				"jobs": [
					{
						"title": "Backend Engineer",
						"isListed": true,
						"isRemote": true,
						"applyUrl": "https://jobs.ashbyhq.com/acme/backend/application"
					}
				]
			}`), nil
		}),
	})
	source.baseURL = "https://ashby.test"

	candidates, err := source.Fetch(t.Context(), SourceConfig{
		Type:         "ashby",
		JobBoardName: "acme",
	})
	if err != nil {
		t.Fatalf("fetch ashby jobs: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected one candidate, got %d", len(candidates))
	}
	if candidates[0].Location != "Remote" {
		t.Fatalf("expected remote location fallback, got %q", candidates[0].Location)
	}
	if candidates[0].URL != "https://jobs.ashbyhq.com/acme/backend/application" {
		t.Fatalf("expected apply URL fallback, got %q", candidates[0].URL)
	}
}

func TestAshbySourceRequiresJobBoardName(t *testing.T) {
	source := NewAshbySource(nil)

	_, err := source.Fetch(t.Context(), SourceConfig{Type: "ashby"})
	if err == nil {
		t.Fatal("expected missing job board name error")
	}
}

func ashbyJSONResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
