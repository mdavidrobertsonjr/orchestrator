package jobs

import "time"

type Status string

const (
	StatusQueued     Status = "queued"
	StatusRunning    Status = "running"
	StatusSucceeded  Status = "succeeded"
	StatusFailed     Status = "failed"
	StatusDeadLetter Status = "dead_letter"
	StatusCanceled   Status = "canceled"
)

type Job struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Type        string            `json:"type"`
	Status      Status            `json:"status"`
	Payload     map[string]any    `json:"payload,omitempty"`
	Attempts    int               `json:"attempts"`
	MaxAttempts int               `json:"max_attempts"`
	Error       string            `json:"error,omitempty"`
	Logs        []LogEntry        `json:"logs,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
	StartedAt   *time.Time        `json:"started_at,omitempty"`
	FinishedAt  *time.Time        `json:"finished_at,omitempty"`
	LeaseOwner  string            `json:"lease_owner,omitempty"`
	LeaseUntil  *time.Time        `json:"lease_until,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

type LogEntry struct {
	Time    time.Time `json:"time"`
	Message string    `json:"message"`
}

type CreateJobParams struct {
	Name        string            `json:"name"`
	Type        string            `json:"type"`
	Payload     map[string]any    `json:"payload"`
	MaxAttempts int               `json:"max_attempts"`
	Metadata    map[string]string `json:"metadata"`
}
