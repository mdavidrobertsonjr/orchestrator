package monitor

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestWorkdaySourceFetchesAndNormalizesJobs(t *testing.T) {
	var requestedPath string
	var requestedMethod string
	source := NewWorkdaySource(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			requestedMethod = req.Method
			requestedPath = req.URL.Path
			return jsonResponse(`{
				"jobPostings": [
					{
						"title": "Software Engineer, New Grad",
						"externalPath": "/en-US/acme/job/software-engineer-new-grad",
						"locationsText": "New York, NY",
						"postedOn": "2026-06-03",
						"bulletFields": ["Engineering", "Full time"]
					}
				]
			}`), nil
		}),
	})
	source.baseURL = "https://%s.workday.test"

	candidates, err := source.Fetch(t.Context(), SourceConfig{
		Type:       "workday",
		Company:    "Acme",
		Tenant:     "acme",
		Site:       "external",
		CareersURL: "https://acme.wd5.myworkdayjobs.com",
		Limit:      25,
	})
	if err != nil {
		t.Fatalf("fetch workday jobs: %v", err)
	}

	if requestedMethod != http.MethodPost {
		t.Fatalf("expected POST request, got %q", requestedMethod)
	}
	if requestedPath != "/wday/cxs/acme/external/jobs" {
		t.Fatalf("unexpected request path %q", requestedPath)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected one candidate, got %d", len(candidates))
	}
	if candidates[0].Company != "Acme" || candidates[0].Title != "Software Engineer, New Grad" {
		t.Fatalf("unexpected candidate: %#v", candidates[0])
	}
	if candidates[0].Source != "workday" || candidates[0].Location != "New York, NY" {
		t.Fatalf("unexpected source/location: %#v", candidates[0])
	}
	if !strings.Contains(candidates[0].URL, "/en-US/acme/job/software-engineer-new-grad") {
		t.Fatalf("unexpected posting URL %q", candidates[0].URL)
	}
}

func TestCustomSourceFetchesWrappedPostings(t *testing.T) {
	source := NewCustomSource(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return jsonResponse(`{
				"jobs": [
					{
						"id": "job-1",
						"company": "Acme",
						"title": "Software Engineer, New Grad",
						"url": "https://example.com/job-1",
						"location": "Remote",
						"posted_at": "2026-06-03T16:00:00Z"
					}
				]
			}`), nil
		}),
	})

	candidates, err := source.Fetch(t.Context(), SourceConfig{Type: "custom", URL: "https://example.com/jobs.json"})
	if err != nil {
		t.Fatalf("fetch custom source: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected one candidate, got %d", len(candidates))
	}
	if candidates[0].Company != "Acme" || candidates[0].Source != "custom" || candidates[0].SourceID != "job-1" {
		t.Fatalf("unexpected candidate: %#v", candidates[0])
	}
	if candidates[0].PostedAt == nil {
		t.Fatalf("expected parsed posted timestamp")
	}
}

func TestSourceRetriesTransientFailures(t *testing.T) {
	attempts := 0
	source := NewGreenhouseSource(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			attempts++
			if attempts == 1 {
				return &http.Response{
					StatusCode: http.StatusTooManyRequests,
					Body:       io.NopCloser(strings.NewReader(`rate limited`)),
				}, nil
			}
			return jsonResponse(`{"jobs":[]}`), nil
		}),
	})

	if _, err := source.Fetch(t.Context(), SourceConfig{Type: "greenhouse", BoardToken: "acme", MaxRetries: 1, BackoffMS: 1}); err != nil {
		t.Fatalf("fetch greenhouse with retry: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("expected two attempts, got %d", attempts)
	}
}
