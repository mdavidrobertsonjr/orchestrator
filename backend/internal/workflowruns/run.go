package workflowruns

import "time"

type Run struct {
	ID             string            `json:"id"`
	WorkflowID     string            `json:"workflow_id"`
	JobID          string            `json:"job_id"`
	Trigger        string            `json:"trigger"`
	Status         string            `json:"status"`
	ScheduledFor   *time.Time        `json:"scheduled_for,omitempty"`
	IdempotencyKey string            `json:"idempotency_key,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
	CreatedAt      time.Time         `json:"created_at"`
	UpdatedAt      time.Time         `json:"updated_at"`
}

type CreateRunParams struct {
	WorkflowID     string            `json:"workflow_id"`
	JobID          string            `json:"job_id"`
	Trigger        string            `json:"trigger"`
	Status         string            `json:"status"`
	ScheduledFor   *time.Time        `json:"scheduled_for"`
	IdempotencyKey string            `json:"idempotency_key"`
	Metadata       map[string]string `json:"metadata"`
}
