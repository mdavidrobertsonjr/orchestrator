package monitor

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestSmartRecruitersSourceFetchesPaginatedPostings(t *testing.T) {
	requests := 0
	source := NewSmartRecruitersSource(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		if req.URL.Path != "/v1/companies/acme/postings" {
			t.Fatalf("unexpected path %s", req.URL.Path)
		}
		if requests == 1 && req.URL.Query().Get("offset") != "0" {
			t.Fatalf("expected first offset 0, got %s", req.URL.Query().Get("offset"))
		}
		if requests == 2 && req.URL.Query().Get("offset") != "1" {
			t.Fatalf("expected second offset 1, got %s", req.URL.Query().Get("offset"))
		}
		body := `{"totalFound":2,"content":[{"id":"1","uuid":"u1","name":"Software Engineer, New Grad","applyUrl":"https://apply.example/1","releasedDate":"2026-09-20T12:00:00Z","location":{"city":"Austin","region":"TX","country":"US"},"company":{"name":"Acme"}}]}`
		if requests == 2 {
			body = `{"totalFound":2,"content":[{"id":"2","name":"Backend Engineer, New Grad","jobAdUrl":"https://jobs.example/2","location":{"remote":true}}]}`
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})})

	got, err := source.Fetch(context.Background(), SourceConfig{CompanyIdentifier: "acme", Limit: 2})
	if err != nil {
		t.Fatalf("fetch smartrecruiters postings: %v", err)
	}
	if len(got) != 2 || got[0].Title != "Software Engineer, New Grad" || got[1].Location != "Remote" {
		t.Fatalf("unexpected postings: %#v", got)
	}
	if requests != 2 {
		t.Fatalf("expected two paginated requests, got %d", requests)
	}
}

func TestSmartRecruitersSourceRequiresIdentifier(t *testing.T) {
	source := NewSmartRecruitersSource(nil)
	if _, err := source.Fetch(context.Background(), SourceConfig{}); err == nil {
		t.Fatal("expected missing company identifier error")
	}
}
