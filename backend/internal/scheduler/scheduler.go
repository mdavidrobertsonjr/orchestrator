package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"orchestrator/backend/internal/jobs"
	"orchestrator/backend/internal/workflowruns"
	"orchestrator/backend/internal/workflows"
)

type Config struct {
	PollInterval time.Duration
}

type Scheduler struct {
	config    Config
	workflows workflows.Store
	runs      workflowruns.Store
	jobs      jobs.Store
	queue     jobs.Queue
	logger    *slog.Logger
}

func New(config Config, workflowStore workflows.Store, jobStore jobs.Store, queue jobs.Queue, logger *slog.Logger) *Scheduler {
	return NewWithRuns(config, workflowStore, nil, jobStore, queue, logger)
}

func NewWithRuns(config Config, workflowStore workflows.Store, runStore workflowruns.Store, jobStore jobs.Store, queue jobs.Queue, logger *slog.Logger) *Scheduler {
	if config.PollInterval <= 0 {
		config.PollInterval = 30 * time.Second
	}
	return &Scheduler{
		config:    config,
		workflows: workflowStore,
		runs:      runStore,
		jobs:      jobStore,
		queue:     queue,
		logger:    logger,
	}
}

func (s *Scheduler) Start(ctx context.Context) {
	go s.loop(ctx)
}

func (s *Scheduler) Tick(ctx context.Context, now time.Time) error {
	if s == nil || s.workflows == nil || s.jobs == nil || s.queue == nil {
		return fmt.Errorf("scheduler is not configured")
	}

	now = now.UTC()
	due, err := s.workflows.ListDue(now)
	if err != nil {
		return err
	}

	for _, workflow := range due {
		if workflow.IntervalSeconds <= 0 {
			s.logWarn("skipping workflow with invalid interval", "workflow_id", workflow.ID, "interval_seconds", workflow.IntervalSeconds)
			continue
		}

		job, err := s.jobs.Create(jobs.CreateJobParams{
			Name:        workflow.Name,
			Type:        workflow.JobType,
			Payload:     workflow.Payload,
			MaxAttempts: workflow.MaxAttempts,
			Metadata:    scheduledMetadata(workflow),
		})
		if err != nil {
			return fmt.Errorf("create scheduled job for workflow %s: %w", workflow.ID, err)
		}

		if err := s.queue.Enqueue(ctx, job.ID); err != nil {
			return fmt.Errorf("enqueue scheduled job %s for workflow %s: %w", job.ID, workflow.ID, err)
		}

		if s.runs != nil {
			if _, err := s.runs.Create(workflowruns.CreateRunParams{
				WorkflowID: workflow.ID,
				JobID:      job.ID,
				Trigger:    "schedule",
				Status:     string(job.Status),
				Metadata:   runMetadata(workflow, job.ID),
			}); err != nil {
				return fmt.Errorf("create workflow run for workflow %s: %w", workflow.ID, err)
			}
		}

		nextRunAt := nextRun(now, time.Duration(workflow.IntervalSeconds)*time.Second)
		if _, err := s.workflows.MarkDispatched(workflow.ID, job.ID, now, nextRunAt); err != nil {
			return fmt.Errorf("mark workflow %s dispatched: %w", workflow.ID, err)
		}

		s.logInfo("scheduled workflow dispatched", "workflow_id", workflow.ID, "job_id", job.ID, "next_run_at", nextRunAt)
	}

	return nil
}

func runMetadata(workflow *workflows.Workflow, jobID string) map[string]string {
	metadata := map[string]string{}
	for key, value := range workflow.Metadata {
		metadata[key] = value
	}
	metadata["workflow_name"] = workflow.Name
	metadata["job_id"] = jobID
	return metadata
}

func (s *Scheduler) loop(ctx context.Context) {
	if err := s.Tick(ctx, time.Now().UTC()); err != nil {
		s.logError("scheduler tick failed", "error", err)
	}

	ticker := time.NewTicker(s.config.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if err := s.Tick(ctx, now); err != nil {
				s.logError("scheduler tick failed", "error", err)
			}
		}
	}
}

func scheduledMetadata(workflow *workflows.Workflow) map[string]string {
	metadata := map[string]string{}
	for key, value := range workflow.Metadata {
		metadata[key] = value
	}
	metadata["submitted_by"] = "scheduler"
	metadata["scheduled"] = "true"
	metadata["workflow_id"] = workflow.ID
	metadata["workflow_name"] = workflow.Name
	return metadata
}

func nextRun(now time.Time, interval time.Duration) time.Time {
	return now.UTC().Add(interval)
}

func (s *Scheduler) logInfo(message string, args ...any) {
	if s.logger != nil {
		s.logger.Info(message, args...)
	}
}

func (s *Scheduler) logWarn(message string, args ...any) {
	if s.logger != nil {
		s.logger.Warn(message, args...)
	}
}

func (s *Scheduler) logError(message string, args ...any) {
	if s.logger != nil {
		s.logger.Error(message, args...)
	}
}
