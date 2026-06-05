package worker

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"orchestrator/backend/internal/email"
	"orchestrator/backend/internal/jobs"
	"orchestrator/backend/internal/monitor"
	"orchestrator/backend/internal/postings"
	"orchestrator/backend/internal/results"
)

func TestSimulatedExecutorLogsEmailReportDetails(t *testing.T) {
	executor := NewSimulatedExecutor(slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil, nil)
	job := &jobs.Job{
		Type: "report.email",
		Payload: map[string]any{
			"duration_ms": float64(100),
			"report": map[string]any{
				"recipients": []any{"ops@example.com"},
				"subject":    "Daily failure report",
				"kind":       "failure_alert",
				"schedule":   "daily at 8am",
			},
		},
	}

	var logs []string
	if err := executor.Execute(t.Context(), job, func(message string) {
		logs = append(logs, message)
	}); err != nil {
		t.Fatalf("execute email report job: %v", err)
	}

	if !containsLog(logs, "email report recipients: ops@example.com") {
		t.Fatalf("expected recipients log, got %#v", logs)
	}
	if !containsLog(logs, "email report subject: Daily failure report") {
		t.Fatalf("expected subject log, got %#v", logs)
	}
	if !containsLog(logs, "email report schedule noted but not yet scheduled: daily at 8am") {
		t.Fatalf("expected schedule log, got %#v", logs)
	}
	if !containsLog(logs, "email delivery provider not configured; simulated report only") {
		t.Fatalf("expected simulated delivery log, got %#v", logs)
	}
}

func TestSimulatedExecutorRunsNewGradMonitor(t *testing.T) {
	postingStore := postings.NewMemoryStore()
	resultStore := results.NewMemoryStore()
	executor := NewSimulatedExecutor(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		monitor.NewRunner(postingStore, nil),
		resultStore,
		nil,
	)
	job := &jobs.Job{
		ID:   "job-1",
		Type: monitor.NewGradJobType,
		Payload: map[string]any{
			"duration_ms": float64(100),
			"sources": []map[string]any{
				{
					"type": "fake",
					"name": "fixture",
					"postings": []map[string]any{
						{
							"company":   "MongoDB",
							"title":     "Software Engineer, New Grad",
							"url":       "https://example.com/mongodb/new-grad",
							"location":  "New York, NY",
							"source":    "greenhouse",
							"source_id": "mdb-1",
						},
					},
				},
			},
			"keywords":  []string{"new grad"},
			"locations": []string{"new york"},
			"min_score": float64(1),
		},
		Metadata: map[string]string{"workflow_id": "workflow-1"},
	}

	var logs []string
	if err := executor.Execute(t.Context(), job, func(message string) {
		logs = append(logs, message)
	}); err != nil {
		t.Fatalf("execute monitor job: %v", err)
	}

	stored, err := postingStore.List()
	if err != nil {
		t.Fatalf("list postings: %v", err)
	}
	if len(stored) != 1 {
		t.Fatalf("expected one stored posting, got %d", len(stored))
	}
	if !containsLog(logs, "monitor completed: scanned=1 matched=1 new=1 updated=0") {
		t.Fatalf("expected monitor completion log, got %#v", logs)
	}

	results, err := resultStore.ListByJob(job.ID)
	if err != nil {
		t.Fatalf("list results: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected one monitor result, got %d", len(results))
	}
	if results[0].WorkflowID != "workflow-1" {
		t.Fatalf("expected workflow result linkage, got %#v", results[0])
	}
	if results[0].Type != "monitor.summary" {
		t.Fatalf("expected monitor summary result, got %#v", results[0])
	}
}

func TestSimulatedExecutorSendsImmediateMonitorAlert(t *testing.T) {
	postingStore := postings.NewMemoryStore()
	resultStore := results.NewMemoryStore()
	sender := &recordingSender{}
	executor := NewSimulatedExecutor(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		monitor.NewRunner(postingStore, nil),
		resultStore,
		sender,
	)
	job := &jobs.Job{
		ID:   "job-1",
		Type: monitor.NewGradJobType,
		Payload: map[string]any{
			"duration_ms": float64(100),
			"sources": []map[string]any{
				{
					"type": "fake",
					"name": "fixture",
					"postings": []map[string]any{
						{
							"company":   "MongoDB",
							"title":     "Software Engineer, New Grad",
							"url":       "https://example.com/mongodb/new-grad",
							"location":  "New York, NY",
							"source":    "greenhouse",
							"source_id": "mdb-1",
						},
					},
				},
			},
			"keywords": []string{"new grad"},
			"notifications": map[string]any{
				"mode":       "immediate",
				"recipients": []any{"me@example.com"},
			},
		},
	}

	if err := executor.Execute(t.Context(), job, func(string) {}); err != nil {
		t.Fatalf("execute monitor job: %v", err)
	}
	if len(sender.messages) != 1 {
		t.Fatalf("expected one alert email, got %d", len(sender.messages))
	}
	if sender.messages[0].Recipients[0] != "me@example.com" {
		t.Fatalf("unexpected recipients: %#v", sender.messages[0].Recipients)
	}

	results, err := resultStore.ListByJob(job.ID)
	if err != nil {
		t.Fatalf("list results: %v", err)
	}
	if len(results) != 1 || results[0].Data["alert_sent"] != true {
		t.Fatalf("expected alert_sent result data, got %#v", results)
	}
}

type recordingSender struct {
	messages []email.Message
}

func (s *recordingSender) Send(ctx context.Context, message email.Message) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	s.messages = append(s.messages, message)
	return nil
}

func containsLog(logs []string, message string) bool {
	for _, log := range logs {
		if log == message {
			return true
		}
	}
	return false
}
