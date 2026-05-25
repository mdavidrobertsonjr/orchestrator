package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"orchestrator/backend/internal/jobs"
)

func TestCreateJobQueuesJob(t *testing.T) {
	store := jobs.NewMemoryStore()
	queue := jobs.NewMemoryQueue(2)
	router := testRouter(store, queue)

	body := bytes.NewBufferString(`{
		"name": "demo",
		"type": "demo.sleep",
		"max_attempts": 2,
		"payload": {"duration_ms": 100},
		"metadata": {"owner": "api-test"}
	}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/jobs", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusAccepted, rec.Code, rec.Body.String())
	}

	var job jobs.Job
	if err := json.NewDecoder(rec.Body).Decode(&job); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if job.ID == "" {
		t.Fatal("expected job ID")
	}
	if job.Status != jobs.StatusQueued {
		t.Fatalf("expected queued status, got %q", job.Status)
	}
	if queue.Len() != 1 {
		t.Fatalf("expected queued job, got queue length %d", queue.Len())
	}

	queuedID, err := queue.Dequeue(req.Context())
	if err != nil {
		t.Fatalf("dequeue job: %v", err)
	}
	if queuedID != job.ID {
		t.Fatalf("expected queued ID %q, got %q", job.ID, queuedID)
	}
}

func TestCreateJobValidation(t *testing.T) {
	router := testRouter(jobs.NewMemoryStore(), jobs.NewMemoryQueue(2))

	tests := []struct {
		name string
		body string
	}{
		{name: "invalid json", body: `{`},
		{name: "missing type", body: `{"name":"demo"}`},
		{name: "negative attempts", body: `{"type":"demo.sleep","max_attempts":-1}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/jobs", bytes.NewBufferString(tt.body))
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected status %d, got %d with body %s", http.StatusBadRequest, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestGetAndListJobs(t *testing.T) {
	store := jobs.NewMemoryStore()
	queue := jobs.NewMemoryQueue(2)
	router := testRouter(store, queue)

	job, err := store.Create(jobs.CreateJobParams{Name: "demo", Type: "demo.sleep"})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/v1/jobs/"+job.ID, nil)
	getRec := httptest.NewRecorder()
	router.ServeHTTP(getRec, getReq)

	if getRec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, getRec.Code, getRec.Body.String())
	}

	var got jobs.Job
	if err := json.NewDecoder(getRec.Body).Decode(&got); err != nil {
		t.Fatalf("decode get response: %v", err)
	}
	if got.ID != job.ID {
		t.Fatalf("expected job ID %q, got %q", job.ID, got.ID)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/v1/jobs", nil)
	listRec := httptest.NewRecorder()
	router.ServeHTTP(listRec, listReq)

	if listRec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, listRec.Code, listRec.Body.String())
	}

	var list struct {
		Jobs []jobs.Job `json:"jobs"`
	}
	if err := json.NewDecoder(listRec.Body).Decode(&list); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(list.Jobs) != 1 || list.Jobs[0].ID != job.ID {
		t.Fatalf("expected list with job %q, got %#v", job.ID, list.Jobs)
	}
}

func TestQueueStatus(t *testing.T) {
	store := jobs.NewMemoryStore()
	queue := jobs.NewMemoryQueue(3)
	router := testRouter(store, queue)

	if err := queue.Enqueue(t.Context(), "job-1"); err != nil {
		t.Fatalf("enqueue job: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/queue", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var response struct {
		Queued   int `json:"queued"`
		Capacity int `json:"capacity"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode queue response: %v", err)
	}
	if response.Queued != 1 || response.Capacity != 3 {
		t.Fatalf("unexpected queue status: %#v", response)
	}
}

func testRouter(store jobs.Store, queue jobs.Queue) http.Handler {
	return NewRouter(Config{
		Queue:  queue,
		Store:  store,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}
