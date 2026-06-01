package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"orchestrator/backend/internal/jobs"
	"orchestrator/backend/internal/llm"
	"orchestrator/backend/internal/workers"
)

type Config struct {
	Queue   jobs.Queue
	Store   jobs.Store
	Workers workers.Registry
	Logger  *slog.Logger
	Static  string
	Planner llm.Planner
}

type Server struct {
	queue   jobs.Queue
	store   jobs.Store
	workers workers.Registry
	logger  *slog.Logger
	planner llm.Planner
}

func NewRouter(config Config) http.Handler {
	server := &Server{
		queue:   config.Queue,
		store:   config.Store,
		workers: config.Workers,
		logger:  config.Logger,
		planner: config.Planner,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", server.handleHealth)
	mux.HandleFunc("GET /v1/jobs", server.handleListJobs)
	mux.HandleFunc("POST /v1/jobs", server.handleCreateJob)
	mux.HandleFunc("POST /v1/jobs/natural", server.handleCreateNaturalJob)
	mux.HandleFunc("GET /v1/jobs/{id}", server.handleGetJob)
	mux.HandleFunc("GET /v1/queue", server.handleQueue)
	mux.HandleFunc("GET /v1/workers", server.handleListWorkers)
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

	metadata := plan.Metadata
	if metadata == nil {
		metadata = map[string]string{}
	}
	metadata["submitted_by"] = "dashboard"
	metadata["submitted_with"] = "natural_language"

	s.createAndEnqueueJob(w, r, jobs.CreateJobParams{
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

func (s *Server) handleListWorkers(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"workers": s.workers.List(),
	})
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
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
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
