package notifications

import "time"

type Delivery struct {
	ID          string         `json:"id"`
	JobID       string         `json:"job_id"`
	WorkflowID  string         `json:"workflow_id"`
	Kind        string         `json:"kind"`
	Provider    string         `json:"provider"`
	Status      string         `json:"status"`
	Recipients  []string       `json:"recipients"`
	Subject     string         `json:"subject"`
	Error       string         `json:"error,omitempty"`
	Attempts    int            `json:"attempts"`
	LastAttempt *time.Time     `json:"last_attempt_at,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

type CreateDeliveryParams struct {
	JobID      string
	WorkflowID string
	Kind       string
	Provider   string
	Recipients []string
	Subject    string
	Metadata   map[string]any
}
