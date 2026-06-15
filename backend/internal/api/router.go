package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"orchestrator/backend/internal/jobs"
	"orchestrator/backend/internal/llm"
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
	Queue     jobs.Queue
	Store     jobs.Store
	Workers   workers.Registry
	Logger    *slog.Logger
	Static    string
	Planner   llm.Planner
	Postings  postings.Store
	Workflows workflows.Store
	Runs      workflowruns.Store
	Results   results.Store
}

type Server struct {
	queue     jobs.Queue
	store     jobs.Store
	workers   workers.Registry
	logger    *slog.Logger
	planner   llm.Planner
	postings  postings.Store
	workflows workflows.Store
	runs      workflowruns.Store
	results   results.Store
}

func NewRouter(config Config) http.Handler {
	server := &Server{
		queue:     config.Queue,
		store:     config.Store,
		workers:   config.Workers,
		logger:    config.Logger,
		planner:   config.Planner,
		postings:  config.Postings,
		workflows: config.Workflows,
		runs:      config.Runs,
		results:   config.Results,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", server.handleHealth)
	mux.HandleFunc("GET /metrics", server.handlePrometheusMetrics)
	mux.HandleFunc("POST /v1/commands/natural", server.handleCreateNaturalCommand)
	mux.HandleFunc("GET /v1/jobs", server.handleListJobs)
	mux.HandleFunc("POST /v1/jobs", server.handleCreateJob)
	mux.HandleFunc("POST /v1/jobs/natural", server.handleCreateNaturalJob)
	mux.HandleFunc("GET /v1/jobs/{id}", server.handleGetJob)
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

	return requestLogger(server.logger, corsMiddleware(mux))
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
	GeneratedAt time.Time         `json:"generated_at"`
	Queue       queueMetrics      `json:"queue"`
	Jobs        jobMetrics        `json:"jobs"`
	Workers     workerMetrics     `json:"workers"`
	Workflows   workflowMetrics   `json:"workflows"`
	Postings    collectionMetrics `json:"postings"`
	Results     collectionMetrics `json:"results"`
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
	Total int `json:"total"`
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
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

	s.createAndEnqueueJob(w, r, jobs.CreateJobParams{
		Name:        req.Name,
		Type:        req.Type,
		Payload:     req.Payload,
		MaxAttempts: req.MaxAttempts,
		Metadata:    req.Metadata,
	})
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

func (s *Server) createAndEnqueueJob(w http.ResponseWriter, r *http.Request, params jobs.CreateJobParams) {
	job, err := s.store.Create(params)
	if err != nil {
		s.logger.Error("failed to create job", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create job")
		return
	}

	if err := s.queue.Enqueue(r.Context(), job.ID); err != nil {
		s.logger.Error("failed to enqueue job", "job_id", job.ID, "error", err)
		writeError(w, http.StatusServiceUnavailable, "queue is unavailable")
		return
	}

	s.logger.Info("job submitted", "job_id", job.ID, "type", job.Type)
	writeJSON(w, http.StatusAccepted, job)
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

func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	jobs, err := s.store.List()
	if err != nil {
		s.logger.Error("failed to list jobs", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list jobs")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"jobs": jobs,
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

	return response, true
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

	postings, err := s.postings.List()
	if err != nil {
		s.logger.Error("failed to list postings", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list postings")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"postings": postings,
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

	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
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

	writeJSON(w, http.StatusOK, map[string]any{
		"results": resultsList,
	})
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
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		}

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
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
