package scheduler

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"orchestrator/backend/internal/jobs"
	"orchestrator/backend/internal/workflowruns"
	"orchestrator/backend/internal/workflows"
)

func TestTickDispatchesDueWorkflow(t *testing.T) {
	workflowStore := workflows.NewMemoryStore()
	runStore := workflowruns.NewMemoryStore()
	jobStore := jobs.NewMemoryStore()
	queue := jobs.NewMemoryQueue(2)
	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	nextRunAt := now.Add(-time.Minute)

	workflow, err := workflowStore.Create(workflows.CreateWorkflowParams{
		Name:            "Datadog monitor",
		JobType:         "jobs.monitor.new_grad",
		Payload:         map[string]any{"min_score": float64(20)},
		Metadata:        map[string]string{"owner": "test"},
		MaxAttempts:     2,
		Enabled:         true,
		IntervalSeconds: 3600,
		NextRunAt:       &nextRunAt,
	})
	if err != nil {
		t.Fatalf("create workflow: %v", err)
	}

	scheduler := NewWithRuns(Config{PollInterval: time.Hour}, workflowStore, runStore, jobStore, queue, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := scheduler.Tick(t.Context(), now); err != nil {
		t.Fatalf("tick scheduler: %v", err)
	}

	if queue.Len() != 1 {
		t.Fatalf("expected queued job, got queue length %d", queue.Len())
	}
	jobID, err := queue.Dequeue(t.Context())
	if err != nil {
		t.Fatalf("dequeue job: %v", err)
	}

	job, err := jobStore.Get(jobID)
	if err != nil {
		t.Fatalf("get scheduled job: %v", err)
	}
	if job.Name != "Datadog monitor" || job.Type != "jobs.monitor.new_grad" {
		t.Fatalf("unexpected scheduled job: %#v", job)
	}
	if job.MaxAttempts != 2 {
		t.Fatalf("expected max attempts 2, got %d", job.MaxAttempts)
	}
	if job.Metadata["submitted_by"] != "scheduler" || job.Metadata["workflow_id"] != workflow.ID {
		t.Fatalf("expected scheduler metadata, got %#v", job.Metadata)
	}

	updated, err := workflowStore.Get(workflow.ID)
	if err != nil {
		t.Fatalf("get workflow: %v", err)
	}
	if updated.LastRunAt == nil || !updated.LastRunAt.Equal(now) {
		t.Fatalf("expected last run %s, got %#v", now, updated.LastRunAt)
	}
	if updated.LastJobID != job.ID {
		t.Fatalf("expected last job %q, got %q", job.ID, updated.LastJobID)
	}
	if !updated.NextRunAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("expected next run one hour later, got %s", updated.NextRunAt)
	}

	runs, err := runStore.ListByWorkflow(workflow.ID)
	if err != nil {
		t.Fatalf("list workflow runs: %v", err)
	}
	if len(runs) != 1 || runs[0].JobID != job.ID || runs[0].Trigger != "schedule" {
		t.Fatalf("expected scheduled workflow run for job %q, got %#v", job.ID, runs)
	}
}

func TestTickSkipsFutureAndDisabledWorkflows(t *testing.T) {
	workflowStore := workflows.NewMemoryStore()
	jobStore := jobs.NewMemoryStore()
	queue := jobs.NewMemoryQueue(2)
	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Minute)
	future := now.Add(time.Minute)

	if _, err := workflowStore.Create(workflows.CreateWorkflowParams{Name: "future", JobType: "demo.sleep", Enabled: true, IntervalSeconds: 60, NextRunAt: &future}); err != nil {
		t.Fatalf("create future workflow: %v", err)
	}
	if _, err := workflowStore.Create(workflows.CreateWorkflowParams{Name: "disabled", JobType: "demo.sleep", Enabled: false, IntervalSeconds: 60, NextRunAt: &past}); err != nil {
		t.Fatalf("create disabled workflow: %v", err)
	}

	scheduler := New(Config{}, workflowStore, jobStore, queue, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := scheduler.Tick(t.Context(), now); err != nil {
		t.Fatalf("tick scheduler: %v", err)
	}
	if queue.Len() != 0 {
		t.Fatalf("expected no queued jobs, got %d", queue.Len())
	}
}

func TestTickIsIdempotentForScheduledTimestamp(t *testing.T) {
	workflowStore := workflows.NewMemoryStore()
	runStore := workflowruns.NewMemoryStore()
	jobStore := jobs.NewMemoryStore()
	queue := jobs.NewMemoryQueue(4)
	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	dueAt := now.Add(-time.Minute)

	if _, err := workflowStore.Create(workflows.CreateWorkflowParams{
		Name:            "due",
		JobType:         "demo.sleep",
		Enabled:         true,
		IntervalSeconds: 60,
		NextRunAt:       &dueAt,
	}); err != nil {
		t.Fatalf("create workflow: %v", err)
	}

	scheduler := NewWithRuns(Config{}, workflowStore, runStore, jobStore, queue, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := scheduler.Tick(t.Context(), now); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	if err := scheduler.Tick(t.Context(), now); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if queue.Len() != 1 {
		t.Fatalf("expected one queued job after duplicate tick, got %d", queue.Len())
	}
	runs, err := runStore.List()
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected one workflow run, got %#v", runs)
	}
}
