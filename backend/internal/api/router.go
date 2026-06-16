package api

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"orchestrator/backend/internal/jobs"
	"orchestrator/backend/internal/llm"
	"orchestrator/backend/internal/notifications"
	"orchestrator/backend/internal/postings"
	"orchestrator/backend/internal/results"
	"orchestrator/backend/internal/workers"
	"orchestrator/backend/internal/workflowruns"
	"orchestrator/backend/internal/workflows"
)

var (
	errCreateJob      = errors.New("create job")
	errEnqueueJob     = errors.New("enqueue job")
	errCreateWorkflow = errors.New("create workflow")
)

type Config struct {
	Queue         jobs.Queue
	Store         jobs.Store
	Workers       workers.Registry
	Logger        *slog.Logger
	Static        string
	Planner       llm.Planner
	Postings      postings.Store
	Workflows     workflows.Store
	Runs          workflowruns.Store
	Results       results.Store
	Notifications notifications.Store
	AuthToken     string
	APIKeys       []APIKey
}

type APIKey struct {
	Name   string
	Token  string
	Scopes []string
}

type Server struct {
	queue         jobs.Queue
	store         jobs.Store
	workers       workers.Registry
	logger        *slog.Logger
	planner       llm.Planner
	postings      postings.Store
	workflows     workflows.Store
	runs          workflowruns.Store
	results       results.Store
	notifications notifications.Store
	apiKeys       []APIKey
}

func NewRouter(config Config) http.Handler {
	server := &Server{
		queue:         config.Queue,
		store:         config.Store,
		workers:       config.Workers,
		logger:        config.Logger,
		planner:       config.Planner,
		postings:      config.Postings,
		workflows:     config.Workflows,
		runs:          config.Runs,
		results:       config.Results,
		notifications: config.Notifications,
		apiKeys:       normalizedAPIKeys(config.AuthToken, config.APIKeys),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", server.handleHealth)
	mux.HandleFunc("GET /readyz", server.handleReady)
	mux.HandleFunc("GET /metrics", server.handlePrometheusMetrics)
	mux.HandleFunc("GET /v1/events", server.handleEvents)
	mux.HandleFunc("POST /v1/commands/natural", server.handleCreateNaturalCommand)
	mux.HandleFunc("GET /v1/jobs", server.handleListJobs)
	mux.HandleFunc("POST /v1/jobs", server.handleCreateJob)
	mux.HandleFunc("POST /v1/jobs/natural", server.handleCreateNaturalJob)
	mux.HandleFunc("GET /v1/jobs/{id}", server.handleGetJob)
	mux.HandleFunc("POST /v1/jobs/{id}/cancel", server.handleCancelJob)
	mux.HandleFunc("POST /v1/jobs/{id}/retry", server.handleRetryJob)
	mux.HandleFunc("GET /v1/queue", server.handleQueue)
	mux.HandleFunc("GET /v1/metrics", server.handleMetrics)
	mux.HandleFunc("GET /v1/workers", server.handleListWorkers)
	mux.HandleFunc("GET /v1/postings", server.handleListPostings)
	mux.HandleFunc("GET /v1/postings/{id}", server.handleGetPosting)
	mux.HandleFunc("GET /v1/workflows", server.handleListWorkflows)
	mux.HandleFunc("POST /v1/workflows", server.handleCreateWorkflow)
	mux.HandleFunc("POST /v1/workflows/natural", server.handleCreateNaturalWorkflow)
	mux.HandleFunc("GET /v1/workflows/{id}", server.handleGetWorkflow)
	mux.HandleFunc("PATCH /v1/workflows/{id}", server.handleUpdateWorkflow)
	mux.HandleFunc("POST /v1/workflows/{id}/run", server.handleRunWorkflow)
	mux.HandleFunc("GET /v1/workflow-runs", server.handleListWorkflowRuns)
	mux.HandleFunc("GET /v1/workflow-runs/{id}", server.handleGetWorkflowRun)
	mux.HandleFunc("GET /v1/results", server.handleListResults)
	mux.HandleFunc("GET /v1/results/{id}", server.handleGetResult)
	if config.Static != "" {
		mux.Handle("GET /", staticHandler(config.Static))
	}

	return requestLogger(server.logger, corsMiddleware(authMiddleware(server.apiKeys, mux)))
}

type createJobRequest struct {
	Name        string            `json:"name"`
	Type        string            `json:"type"`
	Payload     map[string]any    `json:"payload"`
	MaxAttempts int               `json:"max_attempts"`
	Metadata    map[string]string `json:"metadata"`
}

type createNaturalJobRequest struct {
	Prompt string `json:"prompt"`
}

type createWorkflowRequest struct {
	Name            string            `json:"name"`
	JobType         string            `json:"job_type"`
	Payload         map[string]any    `json:"payload"`
	Metadata        map[string]string `json:"metadata"`
	Owner           string            `json:"owner"`
	MaxAttempts     int               `json:"max_attempts"`
	Enabled         *bool             `json:"enabled"`
	IntervalSeconds int               `json:"interval_seconds"`
	NextRunAt       *time.Time        `json:"next_run_at"`
}

type updateWorkflowRequest struct {
	Enabled *bool `json:"enabled"`
}

type naturalCommandResponse struct {
	Action   string              `json:"action"`
	Job      *jobs.Job           `json:"job,omitempty"`
	Workflow *workflows.Workflow `json:"workflow,omitempty"`
}

type metricsResponse struct {
	GeneratedAt   time.Time         `json:"generated_at"`
	Queue         queueMetrics      `json:"queue"`
	Jobs          jobMetrics        `json:"jobs"`
	Workers       workerMetrics     `json:"workers"`
	Workflows     workflowMetrics   `json:"workflows"`
	Postings      collectionMetrics `json:"postings"`
	Results       collectionMetrics `json:"results"`
	Notifications collectionMetrics `json:"notifications"`
	Alerts        []alertMetric     `json:"alerts"`
}

type queueMetrics struct {
	Queued      int     `json:"queued"`
	Capacity    int     `json:"capacity"`
	Utilization float64 `json:"utilization"`
}

type jobMetrics struct {
	Total         int            `json:"total"`
	ByStatus      map[string]int `json:"by_status"`
	Attempts      int            `json:"attempts"`
	RetryAttempts int            `json:"retry_attempts"`
	Leased        int            `json:"leased"`
	ExpiredLeases int            `json:"expired_leases"`
}

type workerMetrics struct {
	Total     int            `json:"total"`
	ByStatus  map[string]int `json:"by_status"`
	Active    int            `json:"active"`
	Running   int            `json:"running"`
	Heartbeat int            `json:"heartbeat"`
}

type workflowMetrics struct {
	Total      int `json:"total"`
	Enabled    int `json:"enabled"`
	Disabled   int `json:"disabled"`
	Due        int `json:"due"`
	RunRecords int `json:"run_records"`
}

type collectionMetrics struct {
	Total  int `json:"total"`
	Failed int `json:"failed,omitempty"`
}

type alertMetric struct {
	Severity string `json:"severity"`
	Name     string `json:"name"`
	Message  string `json:"message"`
	Value    int    `json:"value"`
}

type eventSnapshot struct {
	GeneratedAt time.Time       `json:"generated_at"`
	Queue       queueMetrics    `json:"queue"`
	Jobs        jobMetrics      `json:"jobs"`
	Workers     workerMetrics   `json:"workers"`
	Workflows   workflowMetrics `json:"workflows"`
}

type paginationResponse struct {
	Total    int `json:"total"`
	Limit    int `json:"limit"`
	Offset   int `json:"offset"`
	Returned int `json:"returned"`
}

type jobsPageStore interface {
	ListPage(params jobs.ListParams) ([]*jobs.Job, int, error)
}

type postingsPageStore interface {
	ListPage(params postings.ListParams) ([]*postings.Posting, int, error)
}

type workflowRunsPageStore interface {
	ListPage(params workflowruns.ListParams) ([]*workflowruns.Run, int, error)
}

type resultsPageStore interface {
	ListPage(params results.ListParams) ([]*results.Result, int, error)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	checks := map[string]string{}
	ready := true

	if err := readinessCheck("jobs", checks, func() error {
		_, err := s.store.List()
		return err
	}); err != nil {
		ready = false
	}
	if s.workflows != nil {
		if err := readinessCheck("workflows", checks, func() error {
			_, err := s.workflows.List()
			return err
		}); err != nil {
			ready = false
		}
	}
	if s.runs != nil {
		if err := readinessCheck("workflow_runs", checks, func() error {
			_, err := s.runs.List()
			return err
		}); err != nil {
			ready = false
		}
	}
	if s.postings != nil {
		if err := readinessCheck("postings", checks, func() error {
			_, err := s.postings.List()
			return err
		}); err != nil {
			ready = false
		}
	}
	if s.results != nil {
		if err := readinessCheck("results", checks, func() error {
			_, err := s.results.List()
			return err
		}); err != nil {
			ready = false
		}
	}

	status := http.StatusOK
	state := "ready"
	if !ready {
		status = http.StatusServiceUnavailable
		state = "not_ready"
	}
	writeJSON(w, status, map[string]any{
		"status": state,
		"checks": checks,
	})
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	if !s.writeSnapshotEvent(w) {
		return
	}
	flush(w)

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if !s.writeSnapshotEvent(w) {
				return
			}
			flush(w)
		}
	}
}

func (s *Server) writeSnapshotEvent(w http.ResponseWriter) bool {
	metrics, ok := s.collectMetrics(w)
	if !ok {
		return false
	}
	snapshot := eventSnapshot{
		GeneratedAt: metrics.GeneratedAt,
		Queue:       metrics.Queue,
		Jobs:        metrics.Jobs,
		Workers:     metrics.Workers,
		Workflows:   metrics.Workflows,
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		s.logger.Error("failed to encode event snapshot", "error", err)
		return false
	}
	if _, err := fmt.Fprintf(w, "event: snapshot\ndata: %s\n\n", data); err != nil {
		return false
	}
	return true
}

func flush(w http.ResponseWriter) {
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func readinessCheck(name string, checks map[string]string, check func() error) error {
	if err := check(); err != nil {
		checks[name] = "error"
		return err
	}
	checks[name] = "ok"
	return nil
}

func (s *Server) handleCreateJob(w http.ResponseWriter, r *http.Request) {
	var req createJobRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.Type = strings.TrimSpace(req.Type)
	if req.Type == "" {
		writeError(w, http.StatusBadRequest, "type is required")
		return
	}
	if req.Name == "" {
		req.Name = req.Type
	}
	if req.MaxAttempts < 0 {
		writeError(w, http.StatusBadRequest, "max_attempts cannot be negative")
		return
	}
	req.Metadata = metadataWithOwner(req.Metadata, ownerFromRequest(r))

	job := s.createAndEnqueueJob(w, r, jobs.CreateJobParams{
		Name:        req.Name,
		Type:        req.Type,
		Payload:     req.Payload,
		MaxAttempts: req.MaxAttempts,
		Metadata:    req.Metadata,
	})
	if job != nil {
		s.recordAuditEvent(r, auditEvent{
			Action:  "job.created",
			JobID:   job.ID,
			Summary: "job created: " + job.Name,
			Data: map[string]any{
				"job_type": job.Type,
				"status":   job.Status,
			},
		})
	}
}

func (s *Server) handleCreateNaturalJob(w http.ResponseWriter, r *http.Request) {
	if s.planner == nil {
		writeError(w, http.StatusServiceUnavailable, "LLM planner is not configured")
		return
	}

	var req createNaturalJobRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	req.Prompt = strings.TrimSpace(req.Prompt)
	if req.Prompt == "" {
		writeError(w, http.StatusBadRequest, "prompt is required")
		return
	}

	plan, err := s.planner.Plan(r.Context(), req.Prompt)
	if err != nil {
		s.logger.Error("failed to plan natural language job", "error", err)
		writeError(w, http.StatusBadGateway, "failed to plan job")
		return
	}
	if plan == nil {
		s.logger.Error("failed to plan natural language job", "error", "planner returned nil plan")
		writeError(w, http.StatusBadGateway, "failed to plan job")
		return
	}

	s.createAndEnqueuePlannedJob(w, r, plan)
}

func (s *Server) handleCreateNaturalCommand(w http.ResponseWriter, r *http.Request) {
	if s.planner == nil {
		writeError(w, http.StatusServiceUnavailable, "LLM planner is not configured")
		return
	}
	if s.workflows == nil {
		writeError(w, http.StatusServiceUnavailable, "workflow store is not configured")
		return
	}

	var req createNaturalJobRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	req.Prompt = strings.TrimSpace(req.Prompt)
	if req.Prompt == "" {
		writeError(w, http.StatusBadRequest, "prompt is required")
		return
	}

	plan, err := s.planner.PlanCommand(r.Context(), req.Prompt)
	if err != nil {
		s.logger.Error("failed to plan natural language command", "error", err)
		writeError(w, http.StatusBadGateway, "failed to plan command")
		return
	}
	if plan == nil {
		s.logger.Error("failed to plan natural language command", "error", "planner returned nil plan")
		writeError(w, http.StatusBadGateway, "failed to plan command")
		return
	}

	if plan.Action == "workflow" {
		workflow, err := s.createPlannedWorkflowValue(&plan.Workflow)
		if err != nil {
			s.writeCreateWorkflowError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, naturalCommandResponse{Action: "workflow", Workflow: workflow})
		return
	}

	job, err := s.createAndEnqueuePlannedJobValue(r, &plan.Job)
	if err != nil {
		s.writeCreateJobError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, naturalCommandResponse{Action: "job", Job: job})
}

func (s *Server) createAndEnqueueJob(w http.ResponseWriter, r *http.Request, params jobs.CreateJobParams) *jobs.Job {
	job, err := s.store.Create(params)
	if err != nil {
		s.logger.Error("failed to create job", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create job")
		return nil
	}

	if err := s.queue.Enqueue(r.Context(), job.ID); err != nil {
		s.logger.Error("failed to enqueue job", "job_id", job.ID, "error", err)
		writeError(w, http.StatusServiceUnavailable, "queue is unavailable")
		return nil
	}

	s.logger.Info("job submitted", "job_id", job.ID, "type", job.Type)
	writeJSON(w, http.StatusAccepted, job)
	return job
}

func (s *Server) createAndEnqueuePlannedJob(w http.ResponseWriter, r *http.Request, plan *llm.JobPlan) {
	job, err := s.createAndEnqueuePlannedJobValue(r, plan)
	if err != nil {
		s.writeCreateJobError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func (s *Server) createAndEnqueuePlannedJobValue(r *http.Request, plan *llm.JobPlan) (*jobs.Job, error) {
	metadata := naturalMetadata(plan.Metadata)
	job, err := s.store.Create(jobs.CreateJobParams{
		Name:        plan.Name,
		Type:        plan.Type,
		MaxAttempts: plan.MaxAttempts,
		Payload: map[string]any{
			"duration_ms": plan.DurationMS,
			"report":      plan.Report,
			"should_fail": plan.ShouldFail,
		},
		Metadata: metadata,
	})
	if err != nil {
		s.logger.Error("failed to create planned job", "error", err)
		return nil, errCreateJob
	}

	if err := s.queue.Enqueue(r.Context(), job.ID); err != nil {
		s.logger.Error("failed to enqueue planned job", "job_id", job.ID, "error", err)
		return nil, errEnqueueJob
	}

	s.logger.Info("natural job submitted", "job_id", job.ID, "type", job.Type)
	return job, nil
}

func (s *Server) writeCreateJobError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errEnqueueJob):
		writeError(w, http.StatusServiceUnavailable, "queue is unavailable")
	default:
		writeError(w, http.StatusInternalServerError, "failed to create job")
	}
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	job, err := s.store.Get(r.PathValue("id"))
	if err != nil {
		if errors.Is(err, jobs.ErrNotFound) {
			writeError(w, http.StatusNotFound, "job not found")
			return
		}
		s.logger.Error("failed to get job", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to get job")
		return
	}

	writeJSON(w, http.StatusOK, job)
}

func (s *Server) handleCancelJob(w http.ResponseWriter, r *http.Request) {
	job, err := s.store.MarkCanceled(r.PathValue("id"), "job canceled")
	if err != nil {
		if errors.Is(err, jobs.ErrNotFound) {
			writeError(w, http.StatusNotFound, "queued job not found")
			return
		}
		s.logger.Error("failed to cancel job", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to cancel job")
		return
	}

	s.markRunStatus(job.ID, string(jobs.StatusCanceled))
	s.recordAuditEvent(r, auditEvent{
		Action:  "job.canceled",
		JobID:   job.ID,
		Summary: "job canceled: " + job.Name,
		Data: map[string]any{
			"status": job.Status,
		},
	})
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) handleRetryJob(w http.ResponseWriter, r *http.Request) {
	job, err := s.store.Retry(r.PathValue("id"), "job queued for operator retry")
	if err != nil {
		if errors.Is(err, jobs.ErrNotFound) {
			writeError(w, http.StatusNotFound, "retryable job not found")
			return
		}
		s.logger.Error("failed to retry job", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to retry job")
		return
	}
	if err := s.queue.Enqueue(r.Context(), job.ID); err != nil {
		s.logger.Error("failed to enqueue retried job", "job_id", job.ID, "error", err)
		writeError(w, http.StatusServiceUnavailable, "queue is unavailable")
		return
	}

	s.markRunStatus(job.ID, string(jobs.StatusQueued))
	s.recordAuditEvent(r, auditEvent{
		Action:  "job.retried",
		JobID:   job.ID,
		Summary: "job retried: " + job.Name,
		Data: map[string]any{
			"status": job.Status,
		},
	})
	writeJSON(w, http.StatusAccepted, job)
}

func (s *Server) markRunStatus(jobID string, status string) {
	if s.runs == nil {
		return
	}
	if _, err := s.runs.MarkStatusByJob(jobID, status); err != nil && !errors.Is(err, workflowruns.ErrNotFound) {
		s.logger.Error("failed to update workflow run status", "job_id", jobID, "status", status, "error", err)
	}
}

func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	if pageStore, ok := s.store.(jobsPageStore); ok {
		params, err := jobsListParams(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		page, total, err := pageStore.ListPage(params)
		if err != nil {
			s.logger.Error("failed to list jobs", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to list jobs")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"jobs":       page,
			"pagination": pageResponse(total, params.Limit, params.Offset, len(page)),
		})
		return
	}

	allJobs, err := s.store.List()
	if err != nil {
		s.logger.Error("failed to list jobs", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list jobs")
		return
	}
	filtered := filterJobs(allJobs, r)
	jobsPage, pagination, err := paginate(filtered, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"jobs":       jobsPage,
		"pagination": pagination,
	})
}

func (s *Server) handleQueue(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]int{
		"queued":   s.queue.Len(),
		"capacity": s.queue.Cap(),
	})
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	response, ok := s.collectMetrics(w)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handlePrometheusMetrics(w http.ResponseWriter, r *http.Request) {
	metrics, ok := s.collectMetrics(w)
	if !ok {
		return
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = w.Write([]byte(prometheusMetrics(metrics)))
}

func (s *Server) collectMetrics(w http.ResponseWriter) (metricsResponse, bool) {
	jobs, err := s.store.List()
	if err != nil {
		s.logger.Error("failed to list jobs for metrics", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to collect metrics")
		return metricsResponse{}, false
	}

	now := time.Now().UTC()
	response := metricsResponse{
		GeneratedAt: now,
		Queue: queueMetrics{
			Queued:   s.queue.Len(),
			Capacity: s.queue.Cap(),
		},
		Jobs: jobMetrics{
			ByStatus: map[string]int{},
		},
		Workers: workerMetrics{
			ByStatus: map[string]int{},
		},
	}
	if response.Queue.Capacity > 0 {
		response.Queue.Utilization = float64(response.Queue.Queued) / float64(response.Queue.Capacity)
	}

	for _, job := range jobs {
		response.Jobs.Total++
		response.Jobs.ByStatus[string(job.Status)]++
		response.Jobs.Attempts += job.Attempts
		if job.Attempts > 1 {
			response.Jobs.RetryAttempts += job.Attempts - 1
		}
		if job.LeaseUntil != nil {
			response.Jobs.Leased++
			if !job.LeaseUntil.After(now) {
				response.Jobs.ExpiredLeases++
			}
		}
	}

	for _, worker := range s.workers.List() {
		response.Workers.Total++
		response.Workers.ByStatus[string(worker.Status)]++
		if worker.Status != workers.StatusStopped {
			response.Workers.Active++
		}
		if worker.Status == workers.StatusRunning {
			response.Workers.Running++
		}
		if !worker.LastHeartbeat.IsZero() {
			response.Workers.Heartbeat++
		}
	}

	if s.workflows != nil {
		workflows, err := s.workflows.List()
		if err != nil {
			s.logger.Error("failed to list workflows for metrics", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to collect metrics")
			return metricsResponse{}, false
		}
		response.Workflows.Total = len(workflows)
		for _, workflow := range workflows {
			if workflow.Enabled {
				response.Workflows.Enabled++
				if !workflow.NextRunAt.After(now) {
					response.Workflows.Due++
				}
			} else {
				response.Workflows.Disabled++
			}
		}
	}

	if s.runs != nil {
		runs, err := s.runs.List()
		if err != nil {
			s.logger.Error("failed to list workflow runs for metrics", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to collect metrics")
			return metricsResponse{}, false
		}
		response.Workflows.RunRecords = len(runs)
	}

	if s.postings != nil {
		postings, err := s.postings.List()
		if err != nil {
			s.logger.Error("failed to list postings for metrics", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to collect metrics")
			return metricsResponse{}, false
		}
		response.Postings.Total = len(postings)
	}

	if s.results != nil {
		results, err := s.results.List()
		if err != nil {
			s.logger.Error("failed to list results for metrics", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to collect metrics")
			return metricsResponse{}, false
		}
		response.Results.Total = len(results)
	}

	if s.notifications != nil {
		deliveries, err := s.notifications.List()
		if err != nil {
			s.logger.Error("failed to list notifications for metrics", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to collect metrics")
			return metricsResponse{}, false
		}
		response.Notifications.Total = len(deliveries)
		for _, delivery := range deliveries {
			if delivery.Status == "failed" {
				response.Notifications.Failed++
			}
		}
	}

	response.Alerts = operationalAlerts(response)

	return response, true
}

func operationalAlerts(metrics metricsResponse) []alertMetric {
	var alerts []alertMetric
	if metrics.Jobs.ByStatus[string(jobs.StatusDeadLetter)] > 0 {
		alerts = append(alerts, alertMetric{
			Severity: "critical",
			Name:     "dead_letter_jobs",
			Message:  "one or more jobs are dead-lettered",
			Value:    metrics.Jobs.ByStatus[string(jobs.StatusDeadLetter)],
		})
	}
	if metrics.Jobs.ExpiredLeases > 0 {
		alerts = append(alerts, alertMetric{
			Severity: "critical",
			Name:     "expired_leases",
			Message:  "one or more running job leases have expired",
			Value:    metrics.Jobs.ExpiredLeases,
		})
	}
	if metrics.Workflows.Due > 0 {
		alerts = append(alerts, alertMetric{
			Severity: "warning",
			Name:     "scheduler_lag",
			Message:  "one or more enabled workflows are due",
			Value:    metrics.Workflows.Due,
		})
	}
	if metrics.Notifications.Failed > 0 {
		alerts = append(alerts, alertMetric{
			Severity: "warning",
			Name:     "notification_failures",
			Message:  "one or more notification deliveries failed",
			Value:    metrics.Notifications.Failed,
		})
	}
	return alerts
}

func prometheusMetrics(metrics metricsResponse) string {
	var out bytes.Buffer
	writePromMetric(&out, "orchestrator_queue_queued", nil, float64(metrics.Queue.Queued))
	writePromMetric(&out, "orchestrator_queue_capacity", nil, float64(metrics.Queue.Capacity))
	writePromMetric(&out, "orchestrator_queue_utilization", nil, metrics.Queue.Utilization)
	writePromMetric(&out, "orchestrator_jobs_total", nil, float64(metrics.Jobs.Total))
	for status, count := range metrics.Jobs.ByStatus {
		writePromMetric(&out, "orchestrator_jobs_by_status", map[string]string{"status": status}, float64(count))
	}
	writePromMetric(&out, "orchestrator_job_attempts_total", nil, float64(metrics.Jobs.Attempts))
	writePromMetric(&out, "orchestrator_job_retry_attempts_total", nil, float64(metrics.Jobs.RetryAttempts))
	writePromMetric(&out, "orchestrator_job_leases", nil, float64(metrics.Jobs.Leased))
	writePromMetric(&out, "orchestrator_job_expired_leases", nil, float64(metrics.Jobs.ExpiredLeases))
	writePromMetric(&out, "orchestrator_workers_total", nil, float64(metrics.Workers.Total))
	for status, count := range metrics.Workers.ByStatus {
		writePromMetric(&out, "orchestrator_workers_by_status", map[string]string{"status": status}, float64(count))
	}
	writePromMetric(&out, "orchestrator_workers_active", nil, float64(metrics.Workers.Active))
	writePromMetric(&out, "orchestrator_workers_running", nil, float64(metrics.Workers.Running))
	writePromMetric(&out, "orchestrator_workflows_total", nil, float64(metrics.Workflows.Total))
	writePromMetric(&out, "orchestrator_workflows_enabled", nil, float64(metrics.Workflows.Enabled))
	writePromMetric(&out, "orchestrator_workflows_due", nil, float64(metrics.Workflows.Due))
	writePromMetric(&out, "orchestrator_workflow_runs_total", nil, float64(metrics.Workflows.RunRecords))
	writePromMetric(&out, "orchestrator_postings_total", nil, float64(metrics.Postings.Total))
	writePromMetric(&out, "orchestrator_results_total", nil, float64(metrics.Results.Total))
	writePromMetric(&out, "orchestrator_notifications_total", nil, float64(metrics.Notifications.Total))
	writePromMetric(&out, "orchestrator_notifications_failed", nil, float64(metrics.Notifications.Failed))
	for _, alert := range metrics.Alerts {
		writePromMetric(&out, "orchestrator_operational_alert", map[string]string{"name": alert.Name, "severity": alert.Severity}, float64(alert.Value))
	}
	return out.String()
}

func writePromMetric(out *bytes.Buffer, name string, labels map[string]string, value float64) {
	out.WriteString(name)
	if len(labels) > 0 {
		out.WriteByte('{')
		i := 0
		for key, value := range labels {
			if i > 0 {
				out.WriteByte(',')
			}
			fmt.Fprintf(out, `%s=%q`, key, value)
			i++
		}
		out.WriteByte('}')
	}
	fmt.Fprintf(out, " %g\n", value)
}

func (s *Server) handleListWorkers(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"workers": s.workers.List(),
	})
}

func (s *Server) handleListPostings(w http.ResponseWriter, r *http.Request) {
	if s.postings == nil {
		writeJSON(w, http.StatusOK, map[string]any{"postings": []*postings.Posting{}})
		return
	}

	if pageStore, ok := s.postings.(postingsPageStore); ok {
		params, err := postingsListParams(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		page, total, err := pageStore.ListPage(params)
		if err != nil {
			s.logger.Error("failed to list postings", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to list postings")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"postings":   page,
			"pagination": pageResponse(total, params.Limit, params.Offset, len(page)),
		})
		return
	}

	allPostings, err := s.postings.List()
	if err != nil {
		s.logger.Error("failed to list postings", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list postings")
		return
	}
	filtered := filterPostings(allPostings, r)
	postingsPage, pagination, err := paginate(filtered, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"postings":   postingsPage,
		"pagination": pagination,
	})
}

func (s *Server) handleGetPosting(w http.ResponseWriter, r *http.Request) {
	if s.postings == nil {
		writeError(w, http.StatusNotFound, "posting not found")
		return
	}

	posting, err := s.postings.Get(r.PathValue("id"))
	if err != nil {
		if errors.Is(err, postings.ErrNotFound) {
			writeError(w, http.StatusNotFound, "posting not found")
			return
		}
		s.logger.Error("failed to get posting", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to get posting")
		return
	}

	writeJSON(w, http.StatusOK, posting)
}

func (s *Server) handleCreateWorkflow(w http.ResponseWriter, r *http.Request) {
	if s.workflows == nil {
		writeError(w, http.StatusServiceUnavailable, "workflow store is not configured")
		return
	}

	var req createWorkflowRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.JobType = strings.TrimSpace(req.JobType)
	if req.JobType == "" {
		writeError(w, http.StatusBadRequest, "job_type is required")
		return
	}
	if req.Name == "" {
		req.Name = req.JobType
	}
	if req.MaxAttempts < 0 {
		writeError(w, http.StatusBadRequest, "max_attempts cannot be negative")
		return
	}
	if req.IntervalSeconds <= 0 {
		writeError(w, http.StatusBadRequest, "interval_seconds must be positive")
		return
	}

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	req.Metadata = metadataWithOwner(req.Metadata, firstNonEmptyString(req.Owner, ownerFromRequest(r)))

	workflow, err := s.workflows.Create(workflows.CreateWorkflowParams{
		Name:            req.Name,
		JobType:         req.JobType,
		Payload:         req.Payload,
		Metadata:        req.Metadata,
		MaxAttempts:     req.MaxAttempts,
		Enabled:         enabled,
		IntervalSeconds: req.IntervalSeconds,
		NextRunAt:       req.NextRunAt,
	})
	if err != nil {
		s.logger.Error("failed to create workflow", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create workflow")
		return
	}

	s.logger.Info("workflow created", "workflow_id", workflow.ID, "job_type", workflow.JobType)
	s.recordAuditEvent(r, auditEvent{
		Action:     "workflow.created",
		WorkflowID: workflow.ID,
		Summary:    "workflow created: " + workflow.Name,
		Data: map[string]any{
			"job_type": workflow.JobType,
			"enabled":  workflow.Enabled,
			"owner":    workflow.Metadata["owner"],
		},
	})
	writeJSON(w, http.StatusCreated, workflow)
}

func (s *Server) handleCreateNaturalWorkflow(w http.ResponseWriter, r *http.Request) {
	if s.workflows == nil {
		writeError(w, http.StatusServiceUnavailable, "workflow store is not configured")
		return
	}
	if s.planner == nil {
		writeError(w, http.StatusServiceUnavailable, "LLM planner is not configured")
		return
	}

	var req createNaturalJobRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	req.Prompt = strings.TrimSpace(req.Prompt)
	if req.Prompt == "" {
		writeError(w, http.StatusBadRequest, "prompt is required")
		return
	}

	plan, err := s.planner.PlanWorkflow(r.Context(), req.Prompt)
	if err != nil {
		s.logger.Error("failed to plan natural language workflow", "error", err)
		writeError(w, http.StatusBadGateway, "failed to plan workflow")
		return
	}
	if plan == nil {
		s.logger.Error("failed to plan natural language workflow", "error", "planner returned nil plan")
		writeError(w, http.StatusBadGateway, "failed to plan workflow")
		return
	}

	workflow, err := s.createPlannedWorkflowValue(plan)
	if err != nil {
		s.writeCreateWorkflowError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, workflow)
}

func (s *Server) createPlannedWorkflowValue(plan *llm.WorkflowPlan) (*workflows.Workflow, error) {
	workflow, err := s.workflows.Create(workflows.CreateWorkflowParams{
		Name:            plan.Name,
		JobType:         plan.JobType,
		Payload:         plan.Payload,
		Metadata:        naturalMetadata(plan.Metadata),
		MaxAttempts:     plan.MaxAttempts,
		Enabled:         plan.Enabled,
		IntervalSeconds: plan.IntervalSeconds,
	})
	if err != nil {
		s.logger.Error("failed to create planned workflow", "error", err)
		return nil, errCreateWorkflow
	}

	s.logger.Info("natural workflow created", "workflow_id", workflow.ID, "job_type", workflow.JobType)
	return workflow, nil
}

func (s *Server) writeCreateWorkflowError(w http.ResponseWriter, err error) {
	writeError(w, http.StatusInternalServerError, "failed to create workflow")
}

func naturalMetadata(metadata map[string]string) map[string]string {
	out := map[string]string{}
	for key, value := range metadata {
		out[key] = value
	}
	out["submitted_by"] = "dashboard"
	out["submitted_with"] = "natural_language"
	return out
}

func metadataWithOwner(metadata map[string]string, owner string) map[string]string {
	out := map[string]string{}
	for key, value := range metadata {
		out[key] = value
	}
	owner = strings.TrimSpace(owner)
	if owner != "" && strings.TrimSpace(out["owner"]) == "" {
		out["owner"] = owner
	}
	return out
}

func ownerFromRequest(r *http.Request) string {
	if owner := strings.TrimSpace(r.Header.Get("X-Orchestrator-Owner")); owner != "" {
		return owner
	}
	return authActor(r)
}

type authContextKey struct{}

type authPrincipal struct {
	Name   string
	Scopes map[string]bool
}

func authActor(r *http.Request) string {
	principal, ok := r.Context().Value(authContextKey{}).(authPrincipal)
	if !ok {
		return ""
	}
	return principal.Name
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func (s *Server) handleGetWorkflow(w http.ResponseWriter, r *http.Request) {
	if s.workflows == nil {
		writeError(w, http.StatusNotFound, "workflow not found")
		return
	}

	workflow, err := s.workflows.Get(r.PathValue("id"))
	if err != nil {
		if errors.Is(err, workflows.ErrNotFound) {
			writeError(w, http.StatusNotFound, "workflow not found")
			return
		}
		s.logger.Error("failed to get workflow", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to get workflow")
		return
	}

	writeJSON(w, http.StatusOK, workflow)
}

func (s *Server) handleUpdateWorkflow(w http.ResponseWriter, r *http.Request) {
	if s.workflows == nil {
		writeError(w, http.StatusNotFound, "workflow not found")
		return
	}

	var req updateWorkflowRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Enabled == nil {
		writeError(w, http.StatusBadRequest, "enabled is required")
		return
	}

	workflow, err := s.workflows.SetEnabled(r.PathValue("id"), *req.Enabled)
	if err != nil {
		if errors.Is(err, workflows.ErrNotFound) {
			writeError(w, http.StatusNotFound, "workflow not found")
			return
		}
		s.logger.Error("failed to update workflow", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to update workflow")
		return
	}

	s.recordAuditEvent(r, auditEvent{
		Action:     "workflow.updated",
		WorkflowID: workflow.ID,
		Summary:    "workflow updated: " + workflow.Name,
		Data: map[string]any{
			"enabled": workflow.Enabled,
			"owner":   workflow.Metadata["owner"],
		},
	})
	writeJSON(w, http.StatusOK, workflow)
}

func (s *Server) handleRunWorkflow(w http.ResponseWriter, r *http.Request) {
	if s.workflows == nil {
		writeError(w, http.StatusNotFound, "workflow not found")
		return
	}

	workflow, err := s.workflows.Get(r.PathValue("id"))
	if err != nil {
		if errors.Is(err, workflows.ErrNotFound) {
			writeError(w, http.StatusNotFound, "workflow not found")
			return
		}
		s.logger.Error("failed to get workflow for manual run", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to get workflow")
		return
	}

	metadata := workflowRunMetadata(workflow)
	job, err := s.store.Create(jobs.CreateJobParams{
		Name:        workflow.Name,
		Type:        workflow.JobType,
		Payload:     workflow.Payload,
		MaxAttempts: workflow.MaxAttempts,
		Metadata:    metadata,
	})
	if err != nil {
		s.logger.Error("failed to create manual workflow job", "workflow_id", workflow.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create workflow job")
		return
	}

	if err := s.queue.Enqueue(r.Context(), job.ID); err != nil {
		s.logger.Error("failed to enqueue manual workflow job", "workflow_id", workflow.ID, "job_id", job.ID, "error", err)
		writeError(w, http.StatusServiceUnavailable, "queue is unavailable")
		return
	}

	if s.runs != nil {
		if _, err := s.runs.Create(workflowruns.CreateRunParams{
			WorkflowID: workflow.ID,
			JobID:      job.ID,
			Trigger:    "manual",
			Status:     string(job.Status),
			Metadata: map[string]string{
				"workflow_name": workflow.Name,
				"job_id":        job.ID,
			},
		}); err != nil {
			s.logger.Error("failed to create manual workflow run", "workflow_id", workflow.ID, "job_id", job.ID, "error", err)
			writeError(w, http.StatusInternalServerError, "failed to create workflow run")
			return
		}
	}

	s.logger.Info("workflow manually dispatched", "workflow_id", workflow.ID, "job_id", job.ID)
	s.recordAuditEvent(r, auditEvent{
		Action:     "workflow.run_requested",
		WorkflowID: workflow.ID,
		JobID:      job.ID,
		Summary:    "workflow run requested: " + workflow.Name,
		Data: map[string]any{
			"job_type": workflow.JobType,
			"owner":    workflow.Metadata["owner"],
		},
	})
	writeJSON(w, http.StatusAccepted, job)
}

func workflowRunMetadata(workflow *workflows.Workflow) map[string]string {
	metadata := map[string]string{}
	for key, value := range workflow.Metadata {
		metadata[key] = value
	}
	metadata["submitted_by"] = "dashboard"
	metadata["manual_run"] = "true"
	metadata["workflow_id"] = workflow.ID
	metadata["workflow_name"] = workflow.Name
	return metadata
}

func (s *Server) handleListWorkflows(w http.ResponseWriter, r *http.Request) {
	if s.workflows == nil {
		writeJSON(w, http.StatusOK, map[string]any{"workflows": []*workflows.Workflow{}})
		return
	}

	workflows, err := s.workflows.List()
	if err != nil {
		s.logger.Error("failed to list workflows", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list workflows")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"workflows": workflows,
	})
}

func (s *Server) handleListWorkflowRuns(w http.ResponseWriter, r *http.Request) {
	if s.runs == nil {
		writeJSON(w, http.StatusOK, map[string]any{"runs": []*workflowruns.Run{}})
		return
	}

	if pageStore, ok := s.runs.(workflowRunsPageStore); ok {
		params, err := workflowRunsListParams(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		page, total, err := pageStore.ListPage(params)
		if err != nil {
			s.logger.Error("failed to list workflow runs", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to list workflow runs")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"runs": page, "pagination": pageResponse(total, params.Limit, params.Offset, len(page))})
		return
	}

	var (
		runs []*workflowruns.Run
		err  error
	)
	if workflowID := strings.TrimSpace(r.URL.Query().Get("workflow_id")); workflowID != "" {
		runs, err = s.runs.ListByWorkflow(workflowID)
	} else {
		runs, err = s.runs.List()
	}
	if err != nil {
		s.logger.Error("failed to list workflow runs", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list workflow runs")
		return
	}
	filtered := filterWorkflowRuns(runs, r)
	runsPage, pagination, err := paginate(filtered, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"runs": runsPage, "pagination": pagination})
}

func (s *Server) handleGetWorkflowRun(w http.ResponseWriter, r *http.Request) {
	if s.runs == nil {
		writeError(w, http.StatusNotFound, "workflow run not found")
		return
	}

	run, err := s.runs.Get(r.PathValue("id"))
	if err != nil {
		if errors.Is(err, workflowruns.ErrNotFound) {
			writeError(w, http.StatusNotFound, "workflow run not found")
			return
		}
		s.logger.Error("failed to get workflow run", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to get workflow run")
		return
	}

	writeJSON(w, http.StatusOK, run)
}

func (s *Server) handleListResults(w http.ResponseWriter, r *http.Request) {
	if s.results == nil {
		writeJSON(w, http.StatusOK, map[string]any{"results": []*results.Result{}})
		return
	}

	if pageStore, ok := s.results.(resultsPageStore); ok {
		params, err := resultsListParams(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		page, total, err := pageStore.ListPage(params)
		if err != nil {
			s.logger.Error("failed to list results", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to list results")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"results":    page,
			"pagination": pageResponse(total, params.Limit, params.Offset, len(page)),
		})
		return
	}

	var (
		resultsList []*results.Result
		err         error
	)
	if jobID := strings.TrimSpace(r.URL.Query().Get("job_id")); jobID != "" {
		resultsList, err = s.results.ListByJob(jobID)
	} else if workflowID := strings.TrimSpace(r.URL.Query().Get("workflow_id")); workflowID != "" {
		resultsList, err = s.results.ListByWorkflow(workflowID)
	} else {
		resultsList, err = s.results.List()
	}
	if err != nil {
		s.logger.Error("failed to list results", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list results")
		return
	}
	filtered := filterResults(resultsList, r)
	resultsPage, pagination, err := paginate(filtered, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"results":    resultsPage,
		"pagination": pagination,
	})
}

type auditEvent struct {
	Action     string
	WorkflowID string
	JobID      string
	Summary    string
	Data       map[string]any
}

func (s *Server) recordAuditEvent(r *http.Request, event auditEvent) {
	if s.results == nil {
		return
	}
	actor := ownerFromRequest(r)
	if actor == "" {
		actor = "api"
	}
	data := map[string]any{
		"action": event.Action,
		"actor":  actor,
		"path":   r.URL.Path,
		"method": r.Method,
	}
	for key, value := range event.Data {
		data[key] = value
	}
	if _, err := s.results.Create(results.CreateResultParams{
		JobID:      event.JobID,
		WorkflowID: event.WorkflowID,
		Type:       "audit.event",
		Summary:    event.Summary,
		Data:       data,
	}); err != nil {
		s.logger.Error("failed to record audit event", "action", event.Action, "error", err)
	}
}

func filterJobs(items []*jobs.Job, r *http.Request) []*jobs.Job {
	status := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("status")))
	jobType := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("type")))
	name := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("name")))
	query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	submittedBy := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("submitted_by")))

	out := items[:0]
	for _, job := range items {
		if status != "" && strings.ToLower(string(job.Status)) != status {
			continue
		}
		if jobType != "" && strings.ToLower(job.Type) != jobType {
			continue
		}
		if name != "" && !strings.Contains(strings.ToLower(job.Name), name) {
			continue
		}
		if submittedBy != "" && strings.ToLower(job.Metadata["submitted_by"]) != submittedBy {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(strings.Join([]string{job.ID, job.Name, job.Type, job.Error}, " ")), query) {
			continue
		}
		out = append(out, job)
	}
	return out
}

func filterPostings(items []*postings.Posting, r *http.Request) []*postings.Posting {
	company := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("company")))
	source := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("source")))
	location := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("location")))
	query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	minScore := queryInt(r, "min_score")

	out := items[:0]
	for _, posting := range items {
		if company != "" && !strings.Contains(strings.ToLower(posting.Company), company) {
			continue
		}
		if source != "" && strings.ToLower(posting.Source) != source {
			continue
		}
		if location != "" && !strings.Contains(strings.ToLower(posting.Location), location) {
			continue
		}
		if minScore > 0 && posting.MatchScore < minScore {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(strings.Join([]string{posting.Company, posting.Title, posting.Location, posting.URL}, " ")), query) {
			continue
		}
		out = append(out, posting)
	}
	return out
}

func filterWorkflowRuns(items []*workflowruns.Run, r *http.Request) []*workflowruns.Run {
	jobID := strings.TrimSpace(r.URL.Query().Get("job_id"))
	status := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("status")))
	trigger := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("trigger")))

	out := items[:0]
	for _, run := range items {
		if jobID != "" && run.JobID != jobID {
			continue
		}
		if status != "" && strings.ToLower(run.Status) != status {
			continue
		}
		if trigger != "" && strings.ToLower(run.Trigger) != trigger {
			continue
		}
		out = append(out, run)
	}
	return out
}

func filterResults(items []*results.Result, r *http.Request) []*results.Result {
	resultType := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("type")))
	query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))

	out := items[:0]
	for _, result := range items {
		if resultType != "" && strings.ToLower(result.Type) != resultType {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(strings.Join([]string{result.ID, result.Type, result.Summary}, " ")), query) {
			continue
		}
		out = append(out, result)
	}
	return out
}

func paginate[T any](items []T, r *http.Request) ([]T, paginationResponse, error) {
	limit, offset, err := paginationFromRequest(r)
	if err != nil {
		return nil, paginationResponse{}, err
	}
	if offset > len(items) {
		offset = len(items)
	}

	end := len(items)
	if limit > 0 && offset+limit < end {
		end = offset + limit
	}
	page := items[offset:end]
	return page, pageResponse(len(items), limit, offset, len(page)), nil
}

func paginationFromRequest(r *http.Request) (int, int, error) {
	limit, err := optionalNonNegativeInt(r, "limit")
	if err != nil {
		return 0, 0, err
	}
	offset, err := optionalNonNegativeInt(r, "offset")
	if err != nil {
		return 0, 0, err
	}
	if limit == 0 {
		pageSize, err := optionalNonNegativeInt(r, "page_size")
		if err != nil {
			return 0, 0, err
		}
		limit = pageSize
		if page := queryInt(r, "page"); page > 0 && limit > 0 {
			offset = (page - 1) * limit
		}
	}
	return limit, offset, nil
}

func pageResponse(total int, limit int, offset int, returned int) paginationResponse {
	return paginationResponse{Total: total, Limit: limit, Offset: offset, Returned: returned}
}

func jobsListParams(r *http.Request) (jobs.ListParams, error) {
	limit, offset, err := paginationFromRequest(r)
	if err != nil {
		return jobs.ListParams{}, err
	}
	return jobs.ListParams{
		Status:      strings.TrimSpace(r.URL.Query().Get("status")),
		Type:        strings.TrimSpace(r.URL.Query().Get("type")),
		Name:        strings.TrimSpace(r.URL.Query().Get("name")),
		SubmittedBy: strings.TrimSpace(r.URL.Query().Get("submitted_by")),
		Query:       strings.TrimSpace(r.URL.Query().Get("q")),
		Limit:       limit,
		Offset:      offset,
	}, nil
}

func postingsListParams(r *http.Request) (postings.ListParams, error) {
	limit, offset, err := paginationFromRequest(r)
	if err != nil {
		return postings.ListParams{}, err
	}
	return postings.ListParams{
		Company:  strings.TrimSpace(r.URL.Query().Get("company")),
		Source:   strings.TrimSpace(r.URL.Query().Get("source")),
		Location: strings.TrimSpace(r.URL.Query().Get("location")),
		Query:    strings.TrimSpace(r.URL.Query().Get("q")),
		MinScore: queryInt(r, "min_score"),
		Limit:    limit,
		Offset:   offset,
	}, nil
}

func workflowRunsListParams(r *http.Request) (workflowruns.ListParams, error) {
	limit, offset, err := paginationFromRequest(r)
	if err != nil {
		return workflowruns.ListParams{}, err
	}
	return workflowruns.ListParams{
		WorkflowID: strings.TrimSpace(r.URL.Query().Get("workflow_id")),
		JobID:      strings.TrimSpace(r.URL.Query().Get("job_id")),
		Status:     strings.TrimSpace(r.URL.Query().Get("status")),
		Trigger:    strings.TrimSpace(r.URL.Query().Get("trigger")),
		Limit:      limit,
		Offset:     offset,
	}, nil
}

func resultsListParams(r *http.Request) (results.ListParams, error) {
	limit, offset, err := paginationFromRequest(r)
	if err != nil {
		return results.ListParams{}, err
	}
	return results.ListParams{
		JobID:      strings.TrimSpace(r.URL.Query().Get("job_id")),
		WorkflowID: strings.TrimSpace(r.URL.Query().Get("workflow_id")),
		Type:       strings.TrimSpace(r.URL.Query().Get("type")),
		Query:      strings.TrimSpace(r.URL.Query().Get("q")),
		Limit:      limit,
		Offset:     offset,
	}, nil
}

func optionalNonNegativeInt(r *http.Request, key string) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("%s must be a non-negative integer", key)
	}
	return value, nil
}

func queryInt(r *http.Request, key string) int {
	value, _ := optionalNonNegativeInt(r, key)
	return value
}

func (s *Server) handleGetResult(w http.ResponseWriter, r *http.Request) {
	if s.results == nil {
		writeError(w, http.StatusNotFound, "result not found")
		return
	}

	result, err := s.results.Get(r.PathValue("id"))
	if err != nil {
		if errors.Is(err, results.ErrNotFound) {
			writeError(w, http.StatusNotFound, "result not found")
			return
		}
		s.logger.Error("failed to get result", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to get result")
		return
	}

	writeJSON(w, http.StatusOK, result)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func requestLogger(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
		logger.Info("http request", "method", r.Method, "path", r.URL.Path, "remote_addr", r.RemoteAddr)
	})
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if isAllowedDevOrigin(origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Orchestrator-Token, X-Orchestrator-Owner")
		}

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func authMiddleware(keys []APIKey, next http.Handler) http.Handler {
	keys = normalizedAPIKeys("", keys)
	if len(keys) == 0 {
		return next
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !requiresAuth(r) {
			next.ServeHTTP(w, r)
			return
		}
		principal, ok := authorizedRequest(r, keys)
		if ok && principal.allows(requiredScope(r)) {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), authContextKey{}, principal)))
			return
		}

		w.Header().Set("WWW-Authenticate", `Bearer realm="orchestrator"`)
		if ok {
			writeError(w, http.StatusForbidden, "insufficient API key scope")
			return
		}
		writeError(w, http.StatusUnauthorized, "authentication required")
	})
}

func requiresAuth(r *http.Request) bool {
	path := r.URL.Path
	return path == "/metrics" || path == "/v1" || strings.HasPrefix(path, "/v1/")
}

func authorizedRequest(r *http.Request, keys []APIKey) (authPrincipal, bool) {
	candidates := []string{
		bearerToken(r.Header.Get("Authorization")),
		strings.TrimSpace(r.Header.Get("X-Orchestrator-Token")),
		strings.TrimSpace(r.URL.Query().Get("auth_token")),
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		for _, key := range keys {
			if subtle.ConstantTimeCompare([]byte(candidate), []byte(key.Token)) == 1 {
				return principalFromKey(key), true
			}
		}
	}
	return authPrincipal{}, false
}

func normalizedAPIKeys(legacyToken string, keys []APIKey) []APIKey {
	var out []APIKey
	if strings.TrimSpace(legacyToken) != "" {
		out = append(out, APIKey{Name: "legacy-token", Token: strings.TrimSpace(legacyToken), Scopes: []string{"admin"}})
	}
	for _, key := range keys {
		key.Name = strings.TrimSpace(key.Name)
		key.Token = strings.TrimSpace(key.Token)
		if key.Name == "" {
			key.Name = "api-key"
		}
		if key.Token == "" {
			continue
		}
		out = append(out, key)
	}
	return out
}

func principalFromKey(key APIKey) authPrincipal {
	scopes := map[string]bool{}
	for _, scope := range key.Scopes {
		scope = strings.ToLower(strings.TrimSpace(scope))
		if scope != "" {
			scopes[scope] = true
		}
	}
	if len(scopes) == 0 {
		scopes["admin"] = true
	}
	return authPrincipal{Name: key.Name, Scopes: scopes}
}

func (p authPrincipal) allows(scope string) bool {
	return p.Scopes["admin"] || p.Scopes[strings.ToLower(scope)]
}

func requiredScope(r *http.Request) string {
	if r.URL.Path == "/metrics" || r.URL.Path == "/v1/metrics" {
		return "metrics"
	}
	if r.Method == http.MethodGet {
		return "read"
	}
	return "write"
}

func bearerToken(header string) string {
	header = strings.TrimSpace(header)
	if len(header) < len("Bearer ") || !strings.EqualFold(header[:len("Bearer ")], "Bearer ") {
		return ""
	}
	return strings.TrimSpace(header[len("Bearer "):])
}

func isAllowedDevOrigin(origin string) bool {
	switch origin {
	case "http://localhost:5173", "http://127.0.0.1:5173":
		return true
	default:
		return false
	}
}

func staticHandler(dir string) http.Handler {
	fileServer := http.FileServer(http.Dir(dir))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/") || r.URL.Path == "/v1" {
			http.NotFound(w, r)
			return
		}

		requestPath := filepath.Clean(r.URL.Path)
		if requestPath == "." || requestPath == string(filepath.Separator) {
			serveIndex(w, r, dir)
			return
		}

		filePath := filepath.Join(dir, filepath.FromSlash(strings.TrimPrefix(requestPath, "/")))
		info, err := os.Stat(filePath)
		if err != nil || info.IsDir() {
			serveIndex(w, r, dir)
			return
		}

		fileServer.ServeHTTP(w, r)
	})
}

func serveIndex(w http.ResponseWriter, r *http.Request, dir string) {
	http.ServeFile(w, r, filepath.Join(dir, "index.html"))
}
