package workflowruns

import "time"

type Run struct {
	ID         string            `json:"id"`
	WorkflowID string            `json:"workflow_id"`
	JobID      string            `json:"job_id"`
	Trigger    string            `json:"trigger"`
	Status     string            `json:"status"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	CreatedAt  time.Time         `json:"created_at"`
	UpdatedAt  time.Time         `json:"updated_at"`
}

type CreateRunParams struct {
	WorkflowID string            `json:"workflow_id"`
	JobID      string            `json:"job_id"`
	Trigger    string            `json:"trigger"`
	Status     string            `json:"status"`
	Metadata   map[string]string `json:"metadata"`
}
