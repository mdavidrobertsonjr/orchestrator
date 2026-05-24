package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
