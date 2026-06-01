package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"orchestrator/backend/internal/jobs"
)

type Executor interface {
	Execute(ctx context.Context, job *jobs.Job, logf func(string)) error
}

type SimulatedExecutor struct {
	logger *slog.Logger
}

func NewSimulatedExecutor(logger *slog.Logger) *SimulatedExecutor {
	return &SimulatedExecutor{logger: logger}
}

func (e *SimulatedExecutor) Execute(ctx context.Context, job *jobs.Job, logf func(string)) error {
	duration := durationFromPayload(job.Payload)

	logf(fmt.Sprintf("executor received %q job", job.Type))
	if job.Type == "report.email" {
		logEmailReport(job.Payload, logf)
	}
	logf(fmt.Sprintf("simulating work for %s", duration))

	timer := time.NewTimer(duration)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
	}

	if shouldFail(job.Payload) {
		return errors.New("simulated job failure")
	}

	logf("executor completed work")
	return nil
}

func logEmailReport(payload map[string]any, logf func(string)) {
	report, ok := payload["report"]
	if !ok {
		logf("email report payload missing; using default report")
		return
	}

	data, err := json.Marshal(report)
	if err != nil {
		logf("email report payload could not be encoded")
		return
	}

	var parsed struct {
		Recipients []string `json:"recipients"`
		Subject    string   `json:"subject"`
		Kind       string   `json:"kind"`
		Schedule   string   `json:"schedule"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		logf("email report payload could not be decoded")
		return
	}

	if len(parsed.Recipients) == 0 {
		logf("email report has no recipients; delivery is simulated")
	} else {
		logf("email report recipients: " + strings.Join(parsed.Recipients, ", "))
	}
	if parsed.Subject != "" {
		logf("email report subject: " + parsed.Subject)
	}
	if parsed.Kind != "" {
		logf("email report kind: " + parsed.Kind)
	}
	if parsed.Schedule != "" && parsed.Schedule != "immediate" {
		logf("email report schedule noted but not yet scheduled: " + parsed.Schedule)
	}
	logf("email delivery provider not configured; simulated report only")
}

func durationFromPayload(payload map[string]any) time.Duration {
	raw, ok := payload["duration_ms"]
	if !ok {
		return 1500 * time.Millisecond
	}

	switch value := raw.(type) {
	case float64:
		return boundedDuration(time.Duration(value) * time.Millisecond)
	case int:
		return boundedDuration(time.Duration(value) * time.Millisecond)
	default:
		return 1500 * time.Millisecond
	}
}

func boundedDuration(duration time.Duration) time.Duration {
	if duration < 100*time.Millisecond {
		return 100 * time.Millisecond
	}
	if duration > 30*time.Second {
		return 30 * time.Second
	}
	return duration
}

func shouldFail(payload map[string]any) bool {
	raw, ok := payload["should_fail"]
	if !ok {
		return false
	}

	value, ok := raw.(bool)
	return ok && value
}
