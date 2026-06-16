package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"orchestrator/backend/internal/jobs"
	"orchestrator/backend/internal/llm"
	"orchestrator/backend/internal/postings"
	"orchestrator/backend/internal/results"
	"orchestrator/backend/internal/workers"
	"orchestrator/backend/internal/workflowruns"
	"orchestrator/backend/internal/workflows"
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

func TestReadiness(t *testing.T) {
	router := NewRouter(Config{
		Queue:     jobs.NewMemoryQueue(2),
		Store:     jobs.NewMemoryStore(),
		Workers:   workers.NewMemoryRegistry(),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Postings:  postings.NewMemoryStore(),
		Workflows: workflows.NewMemoryStore(),
		Runs:      workflowruns.NewMemoryStore(),
		Results:   results.NewMemoryStore(),
	})

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var response struct {
		Status string            `json:"status"`
		Checks map[string]string `json:"checks"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Status != "ready" {
		t.Fatalf("expected ready status, got %q", response.Status)
	}
	for _, check := range []string{"jobs", "postings", "workflows", "workflow_runs", "results"} {
		if response.Checks[check] != "ok" {
			t.Fatalf("expected %s check ok, got %#v", check, response.Checks)
		}
	}
}

func TestAuthTokenProtectsOperationalRoutes(t *testing.T) {
	router := NewRouter(Config{
		Queue:     jobs.NewMemoryQueue(2),
		Store:     jobs.NewMemoryStore(),
		Workers:   workers.NewMemoryRegistry(),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		AuthToken: "secret-token",
	})

	healthReq := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	healthRec := httptest.NewRecorder()
	router.ServeHTTP(healthRec, healthReq)
	if healthRec.Code != http.StatusOK {
		t.Fatalf("expected health route to remain public, got %d", healthRec.Code)
	}

	unauthorizedReq := httptest.NewRequest(http.MethodGet, "/v1/jobs", nil)
	unauthorizedRec := httptest.NewRecorder()
	router.ServeHTTP(unauthorizedRec, unauthorizedReq)
	if unauthorizedRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected missing token status %d, got %d", http.StatusUnauthorized, unauthorizedRec.Code)
	}

	authorizedReq := httptest.NewRequest(http.MethodGet, "/v1/jobs", nil)
	authorizedReq.Header.Set("Authorization", "Bearer secret-token")
	authorizedRec := httptest.NewRecorder()
	router.ServeHTTP(authorizedRec, authorizedReq)
	if authorizedRec.Code != http.StatusOK {
		t.Fatalf("expected authorized status %d, got %d with body %s", http.StatusOK, authorizedRec.Code, authorizedRec.Body.String())
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

func TestListJobsFiltersAndPaginates(t *testing.T) {
	store := jobs.NewMemoryStore()
	queue := jobs.NewMemoryQueue(4)
	router := testRouter(store, queue)

	if _, err := store.Create(jobs.CreateJobParams{Name: "first monitor", Type: "jobs.monitor.new_grad", Metadata: map[string]string{"submitted_by": "seed"}}); err != nil {
		t.Fatalf("create job: %v", err)
	}
	if _, err := store.Create(jobs.CreateJobParams{Name: "second report", Type: "report.email", Metadata: map[string]string{"submitted_by": "dashboard"}}); err != nil {
		t.Fatalf("create job: %v", err)
	}
	if _, err := store.Create(jobs.CreateJobParams{Name: "third monitor", Type: "jobs.monitor.new_grad", Metadata: map[string]string{"submitted_by": "seed"}}); err != nil {
		t.Fatalf("create job: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/jobs?type=jobs.monitor.new_grad&submitted_by=seed&limit=1&offset=1", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var response struct {
		Jobs       []jobs.Job         `json:"jobs"`
		Pagination paginationResponse `json:"pagination"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Jobs) != 1 || response.Jobs[0].Type != "jobs.monitor.new_grad" {
		t.Fatalf("unexpected filtered page: %#v", response.Jobs)
	}
	if response.Pagination.Total != 2 || response.Pagination.Limit != 1 || response.Pagination.Offset != 1 || response.Pagination.Returned != 1 {
		t.Fatalf("unexpected pagination metadata: %#v", response.Pagination)
	}
}

func TestListPostingsFiltersAndPaginates(t *testing.T) {
	postingStore := postings.NewMemoryStore()
	router := NewRouter(Config{
		Queue:    jobs.NewMemoryQueue(2),
		Store:    jobs.NewMemoryStore(),
		Workers:  workers.NewMemoryRegistry(),
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Postings: postingStore,
	})

	if _, _, err := postingStore.Upsert(postings.UpsertPostingParams{Company: "OpenAI", Title: "Software Engineer, New Grad", URL: "https://example.com/openai", Source: "ashby", Location: "San Francisco", MatchScore: 40}); err != nil {
		t.Fatalf("upsert posting: %v", err)
	}
	if _, _, err := postingStore.Upsert(postings.UpsertPostingParams{Company: "Stripe", Title: "Account Executive", URL: "https://example.com/stripe", Source: "greenhouse", Location: "New York", MatchScore: 10}); err != nil {
		t.Fatalf("upsert posting: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/postings?source=ashby&min_score=20&q=software&page_size=1&page=1", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var response struct {
		Postings   []postings.Posting `json:"postings"`
		Pagination paginationResponse `json:"pagination"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Postings) != 1 || response.Postings[0].Company != "OpenAI" {
		t.Fatalf("unexpected filtered postings: %#v", response.Postings)
	}
	if response.Pagination.Total != 1 || response.Pagination.Limit != 1 || response.Pagination.Returned != 1 {
		t.Fatalf("unexpected pagination metadata: %#v", response.Pagination)
	}
}

func TestCreateNaturalJobQueuesPlannedJob(t *testing.T) {
	store := jobs.NewMemoryStore()
	queue := jobs.NewMemoryQueue(2)
	router := NewRouter(Config{
		Queue:   queue,
		Store:   store,
		Workers: workers.NewMemoryRegistry(),
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Planner: fakePlanner{plan: &llm.JobPlan{
			Name:        "english transcode",
			Type:        "video.transcode",
			DurationMS:  5000,
			MaxAttempts: 2,
			Metadata:    map[string]string{"source": "test"},
			Report:      llm.ReportPlan{Kind: "custom"},
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/jobs/natural", bytes.NewBufferString(`{"prompt":"transcode for five seconds"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusAccepted, rec.Code, rec.Body.String())
	}
	if queue.Len() != 1 {
		t.Fatalf("expected queued planned job, got queue length %d", queue.Len())
	}

	var job jobs.Job
	if err := json.NewDecoder(rec.Body).Decode(&job); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if job.Name != "english transcode" || job.Type != "video.transcode" {
		t.Fatalf("unexpected planned job: %#v", job)
	}
	if job.Payload["duration_ms"] != float64(5000) {
		t.Fatalf("expected planned duration payload, got %#v", job.Payload)
	}
	if job.Metadata["submitted_with"] != "natural_language" {
		t.Fatalf("expected natural language metadata, got %#v", job.Metadata)
	}
}

func TestCreateNaturalJobWithoutPlanner(t *testing.T) {
	router := testRouter(jobs.NewMemoryStore(), jobs.NewMemoryQueue(2))

	req := httptest.NewRequest(http.MethodPost, "/v1/jobs/natural", bytes.NewBufferString(`{"prompt":"run a job"}`))
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status %d, got %d", http.StatusServiceUnavailable, rec.Code)
	}
}

func TestCreateNaturalWorkflowCreatesPlannedWorkflow(t *testing.T) {
	workflowStore := workflows.NewMemoryStore()
	router := NewRouter(Config{
		Queue:     jobs.NewMemoryQueue(2),
		Store:     jobs.NewMemoryStore(),
		Workers:   workers.NewMemoryRegistry(),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Workflows: workflowStore,
		Planner: fakePlanner{workflowPlan: &llm.WorkflowPlan{
			Name:            "nyc new grad monitor",
			JobType:         "jobs.monitor.new_grad",
			IntervalSeconds: 86400,
			MaxAttempts:     2,
			Enabled:         true,
			Payload:         map[string]any{"min_score": float64(20)},
			Metadata:        map[string]string{"source": "test"},
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/workflows/natural", bytes.NewBufferString(`{"prompt":"monitor nyc new grad roles daily"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusCreated, rec.Code, rec.Body.String())
	}

	var workflow workflows.Workflow
	if err := json.NewDecoder(rec.Body).Decode(&workflow); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if workflow.Name != "nyc new grad monitor" || workflow.JobType != "jobs.monitor.new_grad" {
		t.Fatalf("unexpected planned workflow: %#v", workflow)
	}
	if workflow.Metadata["submitted_with"] != "natural_language" {
		t.Fatalf("expected natural language metadata, got %#v", workflow.Metadata)
	}
}

func TestCreateNaturalWorkflowWithoutPlanner(t *testing.T) {
	router := testRouterWithWorkflows(workflows.NewMemoryStore())

	req := httptest.NewRequest(http.MethodPost, "/v1/workflows/natural", bytes.NewBufferString(`{"prompt":"monitor roles"}`))
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status %d, got %d", http.StatusServiceUnavailable, rec.Code)
	}
}

func TestCreateNaturalCommandCreatesWorkflow(t *testing.T) {
	workflowStore := workflows.NewMemoryStore()
	router := NewRouter(Config{
		Queue:     jobs.NewMemoryQueue(2),
		Store:     jobs.NewMemoryStore(),
		Workers:   workers.NewMemoryRegistry(),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Workflows: workflowStore,
		Planner: fakePlanner{commandPlan: &llm.CommandPlan{
			Action: "workflow",
			Workflow: llm.WorkflowPlan{
				Name:            "datadog monitor",
				JobType:         "jobs.monitor.new_grad",
				IntervalSeconds: 86400,
				MaxAttempts:     2,
				Enabled:         true,
				Payload:         map[string]any{"min_score": float64(20)},
			},
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/commands/natural", bytes.NewBufferString(`{"prompt":"monitor Datadog roles every day"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusCreated, rec.Code, rec.Body.String())
	}

	var response naturalCommandResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Action != "workflow" || response.Workflow == nil {
		t.Fatalf("expected workflow command response, got %#v", response)
	}
	if response.Workflow.JobType != "jobs.monitor.new_grad" {
		t.Fatalf("unexpected workflow response: %#v", response.Workflow)
	}
	if response.Workflow.Metadata["submitted_with"] != "natural_language" {
		t.Fatalf("expected natural language metadata, got %#v", response.Workflow.Metadata)
	}
}

func TestCreateNaturalCommandQueuesJob(t *testing.T) {
	store := jobs.NewMemoryStore()
	queue := jobs.NewMemoryQueue(2)
	router := NewRouter(Config{
		Queue:     queue,
		Store:     store,
		Workers:   workers.NewMemoryRegistry(),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Workflows: workflows.NewMemoryStore(),
		Planner: fakePlanner{commandPlan: &llm.CommandPlan{
			Action: "job",
			Job: llm.JobPlan{
				Name:        "one off report",
				Type:        "report.email",
				DurationMS:  1000,
				MaxAttempts: 1,
				Report:      llm.ReportPlan{Subject: "Report", Kind: "custom"},
			},
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/commands/natural", bytes.NewBufferString(`{"prompt":"send a report now"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusAccepted, rec.Code, rec.Body.String())
	}
	if queue.Len() != 1 {
		t.Fatalf("expected queued job, got queue length %d", queue.Len())
	}

	var response naturalCommandResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Action != "job" || response.Job == nil {
		t.Fatalf("expected job command response, got %#v", response)
	}
	if response.Job.Type != "report.email" {
		t.Fatalf("unexpected job response: %#v", response.Job)
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

func TestCancelQueuedJob(t *testing.T) {
	store := jobs.NewMemoryStore()
	queue := jobs.NewMemoryQueue(2)
	runStore := workflowruns.NewMemoryStore()
	router := NewRouter(Config{
		Queue:   queue,
		Store:   store,
		Workers: workers.NewMemoryRegistry(),
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Runs:    runStore,
	})

	job, err := store.Create(jobs.CreateJobParams{Name: "queued", Type: "demo.sleep"})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	run, err := runStore.Create(workflowruns.CreateRunParams{WorkflowID: "workflow-1", JobID: job.ID, Trigger: "manual"})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/jobs/"+job.ID+"/cancel", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	var canceled jobs.Job
	if err := json.NewDecoder(rec.Body).Decode(&canceled); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if canceled.Status != jobs.StatusCanceled {
		t.Fatalf("expected canceled job, got %#v", canceled)
	}

	updatedRun, err := runStore.Get(run.ID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if updatedRun.Status != string(jobs.StatusCanceled) {
		t.Fatalf("expected canceled run status, got %q", updatedRun.Status)
	}
}

func TestCancelRunningJobReturnsNotFound(t *testing.T) {
	store := jobs.NewMemoryStore()
	router := testRouter(store, jobs.NewMemoryQueue(2))
	job, err := store.Create(jobs.CreateJobParams{Name: "running", Type: "demo.sleep"})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if _, err := store.MarkRunning(job.ID); err != nil {
		t.Fatalf("mark running: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/jobs/"+job.ID+"/cancel", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusNotFound, rec.Code, rec.Body.String())
	}
}

func TestRetryTerminalJob(t *testing.T) {
	store := jobs.NewMemoryStore()
	queue := jobs.NewMemoryQueue(2)
	runStore := workflowruns.NewMemoryStore()
	router := NewRouter(Config{
		Queue:   queue,
		Store:   store,
		Workers: workers.NewMemoryRegistry(),
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Runs:    runStore,
	})

	job, err := store.Create(jobs.CreateJobParams{Name: "dead", Type: "demo.sleep", MaxAttempts: 2})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if _, err := store.MarkRunning(job.ID); err != nil {
		t.Fatalf("mark running: %v", err)
	}
	if _, err := store.MarkDeadLetter(job.ID, "boom"); err != nil {
		t.Fatalf("mark dead letter: %v", err)
	}
	run, err := runStore.Create(workflowruns.CreateRunParams{WorkflowID: "workflow-1", JobID: job.ID, Trigger: "manual", Status: string(jobs.StatusDeadLetter)})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/jobs/"+job.ID+"/retry", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusAccepted, rec.Code, rec.Body.String())
	}
	var retried jobs.Job
	if err := json.NewDecoder(rec.Body).Decode(&retried); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if retried.Status != jobs.StatusQueued || retried.Attempts != 0 {
		t.Fatalf("expected queued retry with reset attempts, got %#v", retried)
	}
	if queue.Len() != 1 {
		t.Fatalf("expected retried job to be enqueued, got queue length %d", queue.Len())
	}

	updatedRun, err := runStore.Get(run.ID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if updatedRun.Status != string(jobs.StatusQueued) {
		t.Fatalf("expected queued run status, got %q", updatedRun.Status)
	}
}

func TestRetryRunningJobReturnsNotFound(t *testing.T) {
	store := jobs.NewMemoryStore()
	router := testRouter(store, jobs.NewMemoryQueue(2))
	job, err := store.Create(jobs.CreateJobParams{Name: "running", Type: "demo.sleep"})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if _, err := store.MarkRunning(job.ID); err != nil {
		t.Fatalf("mark running: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/jobs/"+job.ID+"/retry", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusNotFound, rec.Code, rec.Body.String())
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

func TestMetricsSummarizesRuntimeState(t *testing.T) {
	jobStore := jobs.NewMemoryStore()
	queue := jobs.NewMemoryQueue(5)
	registry := workers.NewMemoryRegistry()
	workflowStore := workflows.NewMemoryStore()
	runStore := workflowruns.NewMemoryStore()
	postingStore := postings.NewMemoryStore()
	resultStore := results.NewMemoryStore()

	firstJob, err := jobStore.Create(jobs.CreateJobParams{Name: "expired", Type: "demo.sleep", MaxAttempts: 3})
	if err != nil {
		t.Fatalf("create first job: %v", err)
	}
	if _, err := jobStore.MarkRunningWithLease(firstJob.ID, "worker-1", time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("mark first job running: %v", err)
	}

	retriedJob, err := jobStore.Create(jobs.CreateJobParams{Name: "retried", Type: "demo.sleep", MaxAttempts: 3})
	if err != nil {
		t.Fatalf("create retried job: %v", err)
	}
	if _, err := jobStore.MarkRunningWithLease(retriedJob.ID, "worker-1", time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("mark retried job running: %v", err)
	}
	if _, err := jobStore.MarkQueued(retriedJob.ID, "retry"); err != nil {
		t.Fatalf("requeue retried job: %v", err)
	}
	if _, err := jobStore.MarkRunningWithLease(retriedJob.ID, "worker-1", time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("mark retried job running again: %v", err)
	}

	queuedJob, err := jobStore.Create(jobs.CreateJobParams{Name: "queued", Type: "demo.sleep"})
	if err != nil {
		t.Fatalf("create queued job: %v", err)
	}
	if err := queue.Enqueue(t.Context(), queuedJob.ID); err != nil {
		t.Fatalf("enqueue queued job: %v", err)
	}

	if _, err := registry.Register("worker-1"); err != nil {
		t.Fatalf("register worker: %v", err)
	}
	if _, err := registry.MarkRunning("worker-1", retriedJob.ID); err != nil {
		t.Fatalf("mark worker running: %v", err)
	}

	past := time.Now().Add(-time.Minute)
	workflow, err := workflowStore.Create(workflows.CreateWorkflowParams{
		Name:            "due workflow",
		JobType:         "demo.sleep",
		Enabled:         true,
		IntervalSeconds: 60,
		NextRunAt:       &past,
	})
	if err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	if _, err := runStore.Create(workflowruns.CreateRunParams{WorkflowID: workflow.ID, JobID: queuedJob.ID, Trigger: "schedule"}); err != nil {
		t.Fatalf("create workflow run: %v", err)
	}
	if _, _, err := postingStore.Upsert(postings.UpsertPostingParams{Company: "Datadog", Title: "SWE", URL: "https://example.com/job", Source: "greenhouse"}); err != nil {
		t.Fatalf("create posting: %v", err)
	}
	if _, err := resultStore.Create(results.CreateResultParams{JobID: queuedJob.ID, Type: "demo.result"}); err != nil {
		t.Fatalf("create result: %v", err)
	}

	router := NewRouter(Config{
		Queue:     queue,
		Store:     jobStore,
		Workers:   registry,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Postings:  postingStore,
		Workflows: workflowStore,
		Runs:      runStore,
		Results:   resultStore,
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/metrics", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var response metricsResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Queue.Queued != 1 || response.Queue.Capacity != 5 || response.Queue.Utilization != 0.2 {
		t.Fatalf("unexpected queue metrics: %#v", response.Queue)
	}
	if response.Jobs.Total != 3 || response.Jobs.ByStatus[string(jobs.StatusRunning)] != 2 || response.Jobs.ByStatus[string(jobs.StatusQueued)] != 1 {
		t.Fatalf("unexpected job status metrics: %#v", response.Jobs)
	}
	if response.Jobs.Attempts != 3 || response.Jobs.RetryAttempts != 1 || response.Jobs.Leased != 2 || response.Jobs.ExpiredLeases != 1 {
		t.Fatalf("unexpected job attempt/lease metrics: %#v", response.Jobs)
	}
	if response.Workers.Total != 1 || response.Workers.Running != 1 || response.Workers.Active != 1 {
		t.Fatalf("unexpected worker metrics: %#v", response.Workers)
	}
	if response.Workflows.Total != 1 || response.Workflows.Enabled != 1 || response.Workflows.Due != 1 || response.Workflows.RunRecords != 1 {
		t.Fatalf("unexpected workflow metrics: %#v", response.Workflows)
	}
	if response.Postings.Total != 1 || response.Results.Total != 1 {
		t.Fatalf("unexpected collection metrics: postings=%#v results=%#v", response.Postings, response.Results)
	}
}

func TestPrometheusMetricsEndpoint(t *testing.T) {
	store := jobs.NewMemoryStore()
	queue := jobs.NewMemoryQueue(2)
	registry := workers.NewMemoryRegistry()
	if _, err := store.Create(jobs.CreateJobParams{Name: "queued", Type: "demo.sleep"}); err != nil {
		t.Fatalf("create job: %v", err)
	}
	if _, err := registry.Register("worker-1"); err != nil {
		t.Fatalf("register worker: %v", err)
	}

	router := testRouterWithWorkers(store, queue, registry)
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	if contentType := rec.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/plain") {
		t.Fatalf("expected text/plain content type, got %q", contentType)
	}

	body := rec.Body.String()
	for _, expected := range []string{
		"orchestrator_jobs_total 1\n",
		`orchestrator_jobs_by_status{status="queued"} 1`,
		"orchestrator_workers_total 1\n",
		`orchestrator_workers_by_status{status="idle"} 1`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("expected metrics body to contain %q, got:\n%s", expected, body)
		}
	}
}

func TestEventsEndpointWritesSnapshot(t *testing.T) {
	store := jobs.NewMemoryStore()
	if _, err := store.Create(jobs.CreateJobParams{Name: "queued", Type: "demo.sleep"}); err != nil {
		t.Fatalf("create job: %v", err)
	}
	router := testRouter(store, jobs.NewMemoryQueue(2))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/v1/events", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	if contentType := rec.Header().Get("Content-Type"); contentType != "text/event-stream" {
		t.Fatalf("expected event stream content type, got %q", contentType)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "event: snapshot") || !strings.Contains(body, `"total":1`) {
		t.Fatalf("expected snapshot event, got:\n%s", body)
	}
}

func TestCORSPreflightForDevFrontend(t *testing.T) {
	router := testRouter(jobs.NewMemoryStore(), jobs.NewMemoryQueue(2))

	req := httptest.NewRequest(http.MethodOptions, "/v1/jobs", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d", http.StatusNoContent, rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
		t.Fatalf("expected dev origin CORS header, got %q", got)
	}
}

func TestListWorkers(t *testing.T) {
	store := jobs.NewMemoryStore()
	queue := jobs.NewMemoryQueue(3)
	registry := workers.NewMemoryRegistry()
	router := testRouterWithWorkers(store, queue, registry)

	if _, err := registry.Register("worker-1"); err != nil {
		t.Fatalf("register worker: %v", err)
	}
	if _, err := registry.MarkRunning("worker-1", "job-1"); err != nil {
		t.Fatalf("mark worker running: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/workers", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var response struct {
		Workers []workers.Worker `json:"workers"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode workers response: %v", err)
	}
	if len(response.Workers) != 1 {
		t.Fatalf("expected 1 worker, got %d", len(response.Workers))
	}
	if response.Workers[0].ID != "worker-1" {
		t.Fatalf("expected worker-1, got %q", response.Workers[0].ID)
	}
	if response.Workers[0].Status != workers.StatusRunning {
		t.Fatalf("expected running status, got %q", response.Workers[0].Status)
	}
	if response.Workers[0].CurrentJobID != "job-1" {
		t.Fatalf("expected current job job-1, got %q", response.Workers[0].CurrentJobID)
	}
}

func TestGetAndListPostings(t *testing.T) {
	postingStore := postings.NewMemoryStore()
	router := NewRouter(Config{
		Queue:    jobs.NewMemoryQueue(2),
		Store:    jobs.NewMemoryStore(),
		Workers:  workers.NewMemoryRegistry(),
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Postings: postingStore,
	})

	posting, isNew, err := postingStore.Upsert(postings.UpsertPostingParams{
		Company:    "Datadog",
		Title:      "Software Engineer, New Grad",
		URL:        "https://example.com/datadog/new-grad",
		Location:   "New York, NY",
		Source:     "greenhouse",
		SourceID:   "dd-1",
		MatchScore: 40,
	})
	if err != nil {
		t.Fatalf("create posting: %v", err)
	}
	if !isNew {
		t.Fatal("expected new posting")
	}

	getReq := httptest.NewRequest(http.MethodGet, "/v1/postings/"+posting.ID, nil)
	getRec := httptest.NewRecorder()
	router.ServeHTTP(getRec, getReq)

	if getRec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, getRec.Code, getRec.Body.String())
	}

	var got postings.Posting
	if err := json.NewDecoder(getRec.Body).Decode(&got); err != nil {
		t.Fatalf("decode get response: %v", err)
	}
	if got.ID != posting.ID {
		t.Fatalf("expected posting ID %q, got %q", posting.ID, got.ID)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/v1/postings", nil)
	listRec := httptest.NewRecorder()
	router.ServeHTTP(listRec, listReq)

	if listRec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, listRec.Code, listRec.Body.String())
	}

	var list struct {
		Postings []postings.Posting `json:"postings"`
	}
	if err := json.NewDecoder(listRec.Body).Decode(&list); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(list.Postings) != 1 || list.Postings[0].ID != posting.ID {
		t.Fatalf("expected list with posting %q, got %#v", posting.ID, list.Postings)
	}
}

func TestCreateWorkflow(t *testing.T) {
	workflowStore := workflows.NewMemoryStore()
	router := testRouterWithWorkflows(workflowStore)

	req := httptest.NewRequest(http.MethodPost, "/v1/workflows", bytes.NewBufferString(`{
		"name": "Datadog monitor",
		"job_type": "jobs.monitor.new_grad",
		"max_attempts": 2,
		"enabled": true,
		"interval_seconds": 3600,
		"payload": {"min_score": 20},
		"metadata": {"owner": "api-test"}
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusCreated, rec.Code, rec.Body.String())
	}

	var workflow workflows.Workflow
	if err := json.NewDecoder(rec.Body).Decode(&workflow); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if workflow.ID == "" {
		t.Fatal("expected workflow ID")
	}
	if workflow.Name != "Datadog monitor" || workflow.JobType != "jobs.monitor.new_grad" {
		t.Fatalf("unexpected workflow: %#v", workflow)
	}
	if !workflow.Enabled {
		t.Fatal("expected workflow to be enabled")
	}
	if workflow.IntervalSeconds != 3600 {
		t.Fatalf("expected interval 3600, got %d", workflow.IntervalSeconds)
	}
	if workflow.MaxAttempts != 2 {
		t.Fatalf("expected max attempts 2, got %d", workflow.MaxAttempts)
	}
}

func TestCreateWorkflowValidation(t *testing.T) {
	router := testRouterWithWorkflows(workflows.NewMemoryStore())

	tests := []struct {
		name string
		body string
	}{
		{name: "invalid json", body: `{`},
		{name: "missing job type", body: `{"interval_seconds":60}`},
		{name: "invalid interval", body: `{"job_type":"demo.sleep","interval_seconds":0}`},
		{name: "negative attempts", body: `{"job_type":"demo.sleep","interval_seconds":60,"max_attempts":-1}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/workflows", bytes.NewBufferString(tt.body))
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected status %d, got %d with body %s", http.StatusBadRequest, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestGetAndListWorkflows(t *testing.T) {
	workflowStore := workflows.NewMemoryStore()
	router := testRouterWithWorkflows(workflowStore)

	workflow, err := workflowStore.Create(workflows.CreateWorkflowParams{
		Name:            "Daily report",
		JobType:         "report.email",
		Enabled:         true,
		IntervalSeconds: 86400,
	})
	if err != nil {
		t.Fatalf("create workflow: %v", err)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/v1/workflows/"+workflow.ID, nil)
	getRec := httptest.NewRecorder()
	router.ServeHTTP(getRec, getReq)

	if getRec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, getRec.Code, getRec.Body.String())
	}

	var got workflows.Workflow
	if err := json.NewDecoder(getRec.Body).Decode(&got); err != nil {
		t.Fatalf("decode get response: %v", err)
	}
	if got.ID != workflow.ID {
		t.Fatalf("expected workflow ID %q, got %q", workflow.ID, got.ID)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/v1/workflows", nil)
	listRec := httptest.NewRecorder()
	router.ServeHTTP(listRec, listReq)

	if listRec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, listRec.Code, listRec.Body.String())
	}

	var list struct {
		Workflows []workflows.Workflow `json:"workflows"`
	}
	if err := json.NewDecoder(listRec.Body).Decode(&list); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(list.Workflows) != 1 || list.Workflows[0].ID != workflow.ID {
		t.Fatalf("expected list with workflow %q, got %#v", workflow.ID, list.Workflows)
	}
}

func TestUpdateWorkflowEnabled(t *testing.T) {
	workflowStore := workflows.NewMemoryStore()
	router := testRouterWithWorkflows(workflowStore)

	workflow, err := workflowStore.Create(workflows.CreateWorkflowParams{
		Name:            "Daily report",
		JobType:         "report.email",
		Enabled:         true,
		IntervalSeconds: 86400,
	})
	if err != nil {
		t.Fatalf("create workflow: %v", err)
	}

	req := httptest.NewRequest(http.MethodPatch, "/v1/workflows/"+workflow.ID, bytes.NewBufferString(`{"enabled":false}`))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var updated workflows.Workflow
	if err := json.NewDecoder(rec.Body).Decode(&updated); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if updated.Enabled {
		t.Fatal("expected disabled workflow")
	}
}

func TestUpdateWorkflowValidation(t *testing.T) {
	router := testRouterWithWorkflows(workflows.NewMemoryStore())

	tests := []struct {
		name string
		body string
	}{
		{name: "invalid json", body: `{`},
		{name: "missing enabled", body: `{}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPatch, "/v1/workflows/missing", bytes.NewBufferString(tt.body))
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected status %d, got %d with body %s", http.StatusBadRequest, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestRunWorkflowCreatesQueuedJob(t *testing.T) {
	workflowStore := workflows.NewMemoryStore()
	jobStore := jobs.NewMemoryStore()
	runStore := workflowruns.NewMemoryStore()
	queue := jobs.NewMemoryQueue(2)
	router := NewRouter(Config{
		Queue:     queue,
		Store:     jobStore,
		Workers:   workers.NewMemoryRegistry(),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Workflows: workflowStore,
		Runs:      runStore,
	})

	workflow, err := workflowStore.Create(workflows.CreateWorkflowParams{
		Name:            "Datadog monitor",
		JobType:         "jobs.monitor.new_grad",
		Payload:         map[string]any{"min_score": float64(20)},
		Metadata:        map[string]string{"team": "recruiting"},
		MaxAttempts:     2,
		Enabled:         true,
		IntervalSeconds: 86400,
	})
	if err != nil {
		t.Fatalf("create workflow: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/workflows/"+workflow.ID+"/run", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusAccepted, rec.Code, rec.Body.String())
	}

	var job jobs.Job
	if err := json.NewDecoder(rec.Body).Decode(&job); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if job.Type != workflow.JobType || job.Name != workflow.Name {
		t.Fatalf("unexpected job created from workflow: %#v", job)
	}
	if job.Metadata["workflow_id"] != workflow.ID || job.Metadata["manual_run"] != "true" {
		t.Fatalf("expected manual workflow metadata, got %#v", job.Metadata)
	}
	if queue.Len() != 1 {
		t.Fatalf("expected queued job, got queue length %d", queue.Len())
	}

	runs, err := runStore.ListByWorkflow(workflow.ID)
	if err != nil {
		t.Fatalf("list workflow runs: %v", err)
	}
	if len(runs) != 1 || runs[0].JobID != job.ID || runs[0].Trigger != "manual" {
		t.Fatalf("expected manual workflow run for job %q, got %#v", job.ID, runs)
	}
}

func TestListWorkflowRuns(t *testing.T) {
	runStore := workflowruns.NewMemoryStore()
	run, err := runStore.Create(workflowruns.CreateRunParams{WorkflowID: "workflow-1", JobID: "job-1", Trigger: "manual"})
	if err != nil {
		t.Fatalf("create workflow run: %v", err)
	}
	router := NewRouter(Config{
		Queue:   jobs.NewMemoryQueue(2),
		Store:   jobs.NewMemoryStore(),
		Workers: workers.NewMemoryRegistry(),
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Runs:    runStore,
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/workflow-runs?workflow_id=workflow-1", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var response struct {
		Runs []workflowruns.Run `json:"runs"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Runs) != 1 || response.Runs[0].ID != run.ID {
		t.Fatalf("expected workflow run %q, got %#v", run.ID, response.Runs)
	}
}

func TestGetAndListResults(t *testing.T) {
	resultStore := results.NewMemoryStore()
	router := testRouterWithResults(resultStore)

	result, err := resultStore.Create(results.CreateResultParams{
		JobID:      "job-1",
		WorkflowID: "workflow-1",
		Type:       "monitor.summary",
		Summary:    "1 new match",
		Data:       map[string]any{"created": float64(1)},
	})
	if err != nil {
		t.Fatalf("create result: %v", err)
	}
	if _, err := resultStore.Create(results.CreateResultParams{JobID: "job-2", WorkflowID: "workflow-2", Type: "monitor.summary"}); err != nil {
		t.Fatalf("create second result: %v", err)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/v1/results/"+result.ID, nil)
	getRec := httptest.NewRecorder()
	router.ServeHTTP(getRec, getReq)

	if getRec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, getRec.Code, getRec.Body.String())
	}

	var got results.Result
	if err := json.NewDecoder(getRec.Body).Decode(&got); err != nil {
		t.Fatalf("decode get response: %v", err)
	}
	if got.ID != result.ID {
		t.Fatalf("expected result ID %q, got %q", result.ID, got.ID)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/v1/results?workflow_id=workflow-1", nil)
	listRec := httptest.NewRecorder()
	router.ServeHTTP(listRec, listReq)

	if listRec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, listRec.Code, listRec.Body.String())
	}

	var list struct {
		Results []results.Result `json:"results"`
	}
	if err := json.NewDecoder(listRec.Body).Decode(&list); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(list.Results) != 1 || list.Results[0].ID != result.ID {
		t.Fatalf("expected filtered result %q, got %#v", result.ID, list.Results)
	}
}

func TestStaticDashboardServing(t *testing.T) {
	staticDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(staticDir, "index.html"), []byte("<!doctype html><div id=\"root\"></div>"), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}
	if err := os.Mkdir(filepath.Join(staticDir, "assets"), 0o755); err != nil {
		t.Fatalf("create assets dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(staticDir, "assets", "app.js"), []byte("console.log('ok')"), 0o644); err != nil {
		t.Fatalf("write asset: %v", err)
	}

	router := testRouterWithStatic(staticDir)

	rootReq := httptest.NewRequest(http.MethodGet, "/", nil)
	rootRec := httptest.NewRecorder()
	router.ServeHTTP(rootRec, rootReq)
	if rootRec.Code != http.StatusOK {
		t.Fatalf("expected root status %d, got %d", http.StatusOK, rootRec.Code)
	}
	if !strings.Contains(rootRec.Body.String(), `id="root"`) {
		t.Fatalf("expected index html, got %q", rootRec.Body.String())
	}

	assetReq := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
	assetRec := httptest.NewRecorder()
	router.ServeHTTP(assetRec, assetReq)
	if assetRec.Code != http.StatusOK {
		t.Fatalf("expected asset status %d, got %d", http.StatusOK, assetRec.Code)
	}
	if assetRec.Body.String() != "console.log('ok')" {
		t.Fatalf("expected asset body, got %q", assetRec.Body.String())
	}

	healthReq := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	healthRec := httptest.NewRecorder()
	router.ServeHTTP(healthRec, healthReq)
	if healthRec.Code != http.StatusOK {
		t.Fatalf("expected health route to remain available, got %d", healthRec.Code)
	}

	apiReq := httptest.NewRequest(http.MethodGet, "/v1/missing", nil)
	apiRec := httptest.NewRecorder()
	router.ServeHTTP(apiRec, apiReq)
	if apiRec.Code != http.StatusNotFound {
		t.Fatalf("expected unknown API route status %d, got %d", http.StatusNotFound, apiRec.Code)
	}
}

func testRouter(store jobs.Store, queue jobs.Queue) http.Handler {
	return testRouterWithWorkers(store, queue, workers.NewMemoryRegistry())
}

func testRouterWithWorkers(store jobs.Store, queue jobs.Queue, registry workers.Registry) http.Handler {
	return NewRouter(Config{
		Queue:   queue,
		Store:   store,
		Workers: registry,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

func testRouterWithStatic(staticDir string) http.Handler {
	return NewRouter(Config{
		Queue:   jobs.NewMemoryQueue(2),
		Store:   jobs.NewMemoryStore(),
		Workers: workers.NewMemoryRegistry(),
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Static:  staticDir,
	})
}

func testRouterWithWorkflows(workflowStore workflows.Store) http.Handler {
	return NewRouter(Config{
		Queue:     jobs.NewMemoryQueue(2),
		Store:     jobs.NewMemoryStore(),
		Workers:   workers.NewMemoryRegistry(),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Workflows: workflowStore,
	})
}

func testRouterWithResults(resultStore results.Store) http.Handler {
	return NewRouter(Config{
		Queue:   jobs.NewMemoryQueue(2),
		Store:   jobs.NewMemoryStore(),
		Workers: workers.NewMemoryRegistry(),
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Results: resultStore,
	})
}

type fakePlanner struct {
	plan         *llm.JobPlan
	workflowPlan *llm.WorkflowPlan
	commandPlan  *llm.CommandPlan
	err          error
}

func (p fakePlanner) Plan(context.Context, string) (*llm.JobPlan, error) {
	return p.plan, p.err
}

func (p fakePlanner) PlanWorkflow(context.Context, string) (*llm.WorkflowPlan, error) {
	return p.workflowPlan, p.err
}

func (p fakePlanner) PlanCommand(context.Context, string) (*llm.CommandPlan, error) {
	return p.commandPlan, p.err
}
