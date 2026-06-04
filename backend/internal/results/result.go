package results

import "time"

type Result struct {
	ID         string         `json:"id"`
	JobID      string         `json:"job_id,omitempty"`
	WorkflowID string         `json:"workflow_id,omitempty"`
	Type       string         `json:"type"`
	Summary    string         `json:"summary,omitempty"`
	Data       map[string]any `json:"data,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
}

type CreateResultParams struct {
	JobID      string         `json:"job_id"`
	WorkflowID string         `json:"workflow_id"`
	Type       string         `json:"type"`
	Summary    string         `json:"summary"`
	Data       map[string]any `json:"data"`
}
