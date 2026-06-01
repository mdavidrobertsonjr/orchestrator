package worker

import (
	"io"
	"log/slog"
	"testing"

	"orchestrator/backend/internal/jobs"
)

func TestSimulatedExecutorLogsEmailReportDetails(t *testing.T) {
	executor := NewSimulatedExecutor(slog.New(slog.NewTextHandler(io.Discard, nil)))
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

func containsLog(logs []string, message string) bool {
	for _, log := range logs {
		if log == message {
			return true
		}
	}
	return false
}
