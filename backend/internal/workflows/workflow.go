package workflows

import "time"

type Workflow struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	JobType         string            `json:"job_type"`
	Payload         map[string]any    `json:"payload,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
	MaxAttempts     int               `json:"max_attempts"`
	Enabled         bool              `json:"enabled"`
	IntervalSeconds int               `json:"interval_seconds"`
	NextRunAt       time.Time         `json:"next_run_at"`
	LastRunAt       *time.Time        `json:"last_run_at,omitempty"`
	LastJobID       string            `json:"last_job_id,omitempty"`
	CreatedAt       time.Time         `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
}

type CreateWorkflowParams struct {
	Name            string            `json:"name"`
	JobType         string            `json:"job_type"`
	Payload         map[string]any    `json:"payload"`
	Metadata        map[string]string `json:"metadata"`
	MaxAttempts     int               `json:"max_attempts"`
	Enabled         bool              `json:"enabled"`
	IntervalSeconds int               `json:"interval_seconds"`
	NextRunAt       *time.Time        `json:"next_run_at"`
}
