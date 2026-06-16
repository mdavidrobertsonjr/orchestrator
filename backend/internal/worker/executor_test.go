package worker

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
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

func TestSimulatedExecutorRunsHTTPRequest(t *testing.T) {
	resultStore := results.NewMemoryStore()
	executor := NewSimulatedExecutor(slog.New(slog.NewTextHandler(io.Discard, nil)), nil, resultStore, nil)
	executor.SetHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		if r.Header.Get("X-Test") != "yes" {
			t.Fatalf("expected X-Test header")
		}
		return response(http.StatusCreated, "created"), nil
	})})
	job := &jobs.Job{
		ID:   "job-1",
		Type: "http.request",
		Payload: map[string]any{
			"duration_ms": float64(100),
			"method":      "POST",
			"url":         "https://example.com/create",
			"headers": map[string]any{
				"X-Test": "yes",
			},
			"body": "payload",
		},
		Metadata: map[string]string{"workflow_id": "workflow-1"},
	}

	var logs []string
	if err := executor.Execute(t.Context(), job, func(message string) {
		logs = append(logs, message)
	}); err != nil {
		t.Fatalf("execute http request: %v", err)
	}
	if !containsLog(logs, "http request completed with status 201") {
		t.Fatalf("expected http completion log, got %#v", logs)
	}

	stored, err := resultStore.ListByJob(job.ID)
	if err != nil {
		t.Fatalf("list results: %v", err)
	}
	if len(stored) != 1 || stored[0].Type != "http.response" || stored[0].WorkflowID != "workflow-1" {
		t.Fatalf("expected http response result, got %#v", stored)
	}
	if stored[0].Data["status_code"] != 201 {
		t.Fatalf("expected status code result data, got %#v", stored[0].Data)
	}
}

func TestSimulatedExecutorHTTPRequestFailsOnServerError(t *testing.T) {
	executor := NewSimulatedExecutor(slog.New(slog.NewTextHandler(io.Discard, nil)), nil, results.NewMemoryStore(), nil)
	executor.SetHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return response(http.StatusServiceUnavailable, ""), nil
	})})
	job := &jobs.Job{
		ID:   "job-1",
		Type: "http.request",
		Payload: map[string]any{
			"duration_ms": float64(100),
			"url":         "https://example.com/unavailable",
		},
	}

	err := executor.Execute(t.Context(), job, func(string) {})
	if err == nil || !strings.Contains(err.Error(), "retryable status 503") {
		t.Fatalf("expected retryable status error, got %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func response(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
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

func TestMonitorNotificationPreferences(t *testing.T) {
	config := monitorNotificationConfig(map[string]any{
		"notifications": map[string]any{
			"mode":                    "digest_only",
			"recipients":              []any{"me@example.com"},
			"quiet_hours_start":       "22:00",
			"quiet_hours_end":         "07:00",
			"timezone":                "America/New_York",
			"max_alerts_per_workflow": float64(1),
		},
	})

	if config.mode != "digest_only" {
		t.Fatalf("expected digest_only mode, got %q", config.mode)
	}
	if config.quietHoursStart != "22:00" || config.quietHoursEnd != "07:00" || config.timezone != "America/New_York" {
		t.Fatalf("unexpected quiet hour config: %#v", config)
	}
	if config.maxAlertsPerWorkflow != 1 {
		t.Fatalf("expected max alerts 1, got %d", config.maxAlertsPerWorkflow)
	}
}

func TestSimulatedExecutorDetectsMaxMonitorAlerts(t *testing.T) {
	resultStore := results.NewMemoryStore()
	if _, err := resultStore.Create(results.CreateResultParams{
		WorkflowID: "workflow-1",
		Type:       "monitor.summary",
		Data:       map[string]any{"alert_sent": true},
	}); err != nil {
		t.Fatalf("create result: %v", err)
	}
	executor := NewSimulatedExecutor(slog.New(slog.NewTextHandler(io.Discard, nil)), nil, resultStore, nil)
	job := &jobs.Job{Metadata: map[string]string{"workflow_id": "workflow-1"}}

	if !executor.maxAlertsReached(job, notificationConfig{maxAlertsPerWorkflow: 1}) {
		t.Fatal("expected max alerts to be reached")
	}
}

func TestSimulatedExecutorUsesDefaultMonitorAlertRecipients(t *testing.T) {
	postingStore := postings.NewMemoryStore()
	sender := &recordingSender{}
	executor := NewSimulatedExecutor(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		monitor.NewRunner(postingStore, nil),
		results.NewMemoryStore(),
		sender,
	)
	executor.SetDefaultRecipients([]string{" default@example.com "})

	job := &jobs.Job{
		ID:   "job-1",
		Type: monitor.NewGradJobType,
		Payload: map[string]any{
			"duration_ms": float64(100),
			"sources": []map[string]any{
				{
					"type": "fake",
					"postings": []map[string]any{
						{
							"company":   "Datadog",
							"title":     "Software Engineer, New Grad",
							"url":       "https://example.com/datadog/new-grad",
							"location":  "New York, NY",
							"source":    "fake",
							"source_id": "dd-1",
						},
					},
				},
			},
			"keywords":          []string{"new grad"},
			"notification_mode": "immediate",
		},
	}

	if err := executor.Execute(t.Context(), job, func(string) {}); err != nil {
		t.Fatalf("execute monitor job: %v", err)
	}
	if len(sender.messages) != 1 {
		t.Fatalf("expected one alert email, got %d", len(sender.messages))
	}
	if sender.messages[0].Recipients[0] != "default@example.com" {
		t.Fatalf("unexpected recipients: %#v", sender.messages[0].Recipients)
	}
}

func TestSimulatedExecutorSendsMonitorDigestReport(t *testing.T) {
	resultStore := results.NewMemoryStore()
	if _, err := resultStore.Create(results.CreateResultParams{
		JobID:      "job-1",
		WorkflowID: "workflow-1",
		Type:       "monitor.summary",
		Summary:    "scanned 2 postings, matched 1, new 1",
	}); err != nil {
		t.Fatalf("create result: %v", err)
	}

	sender := &recordingSender{}
	executor := NewSimulatedExecutor(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		nil,
		resultStore,
		sender,
	)
	job := &jobs.Job{
		Type: "report.email",
		Payload: map[string]any{
			"duration_ms": float64(100),
			"report": map[string]any{
				"recipients": []any{"me@example.com"},
				"subject":    "Daily monitor digest",
				"kind":       "monitor_digest",
				"schedule":   "daily",
			},
		},
	}

	if err := executor.Execute(t.Context(), job, func(string) {}); err != nil {
		t.Fatalf("execute digest report: %v", err)
	}
	if len(sender.messages) != 1 {
		t.Fatalf("expected one digest email, got %d", len(sender.messages))
	}
	if !strings.Contains(sender.messages[0].Body, "scanned 2 postings, matched 1, new 1") {
		t.Fatalf("expected digest body to include monitor result, got %q", sender.messages[0].Body)
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
