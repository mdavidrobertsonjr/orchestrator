package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"orchestrator/backend/internal/email"
	"orchestrator/backend/internal/jobs"
	"orchestrator/backend/internal/monitor"
	"orchestrator/backend/internal/results"
)

type Executor interface {
	Execute(ctx context.Context, job *jobs.Job, logf func(string)) error
}

type SimulatedExecutor struct {
	logger        *slog.Logger
	monitorRunner *monitor.Runner
	results       results.Store
}

func NewSimulatedExecutor(logger *slog.Logger, monitorRunner *monitor.Runner, resultStore results.Store) *SimulatedExecutor {
	return &SimulatedExecutor{logger: logger, monitorRunner: monitorRunner, results: resultStore}
}

func (e *SimulatedExecutor) Execute(ctx context.Context, job *jobs.Job, logf func(string)) error {
	duration := durationFromPayload(job.Payload)

	logf(fmt.Sprintf("executor received %q job", job.Type))
	if job.Type == monitor.NewGradJobType {
		result, err := e.monitorRunner.Run(ctx, job.Payload, logf)
		if err != nil {
			return err
		}
		if err := e.recordMonitorResult(job, result, logf); err != nil {
			return err
		}
	} else if job.Type == "report.email" {
		if err := sendEmailReport(ctx, job.Payload, email.NewSimulatedSender(logf), logf); err != nil {
			return err
		}
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

func (e *SimulatedExecutor) recordMonitorResult(job *jobs.Job, result *monitor.Result, logf func(string)) error {
	if e.results == nil || result == nil {
		return nil
	}

	workflowID := ""
	if job.Metadata != nil {
		workflowID = job.Metadata["workflow_id"]
	}

	created, err := e.results.Create(results.CreateResultParams{
		JobID:      job.ID,
		WorkflowID: workflowID,
		Type:       "monitor.summary",
		Summary:    fmt.Sprintf("scanned %d postings, matched %d, new %d", result.Scanned, result.Matched, result.Created),
		Data: map[string]any{
			"scanned": result.Scanned,
			"matched": result.Matched,
			"created": result.Created,
			"updated": result.Updated,
		},
	})
	if err != nil {
		return err
	}

	logf("recorded monitor result: " + created.ID)
	return nil
}

func sendEmailReport(ctx context.Context, payload map[string]any, sender email.Sender, logf func(string)) error {
	report, ok := payload["report"]
	if !ok {
		logf("email report payload missing; using default report")
		return sender.Send(ctx, email.Message{
			Subject:  "Orchestrator report",
			Metadata: map[string]string{"kind": "job_summary", "schedule": "immediate"},
		})
	}

	data, err := json.Marshal(report)
	if err != nil {
		logf("email report payload could not be encoded")
		return err
	}

	var parsed struct {
		Recipients []string `json:"recipients"`
		Subject    string   `json:"subject"`
		Kind       string   `json:"kind"`
		Schedule   string   `json:"schedule"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		logf("email report payload could not be decoded")
		return err
	}

	return sender.Send(ctx, email.Message{
		Recipients: parsed.Recipients,
		Subject:    parsed.Subject,
		Body:       "Simulated orchestrator report.",
		Metadata: map[string]string{
			"kind":     parsed.Kind,
			"schedule": parsed.Schedule,
		},
	})
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
