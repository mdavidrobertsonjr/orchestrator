package workflows

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const postgresStoreTimeout = 5 * time.Second

type PostgresStore struct {
	db *sql.DB
}

func NewPostgresStore(ctx context.Context, databaseURL string) (*PostgresStore, error) {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, err
	}

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}

	return &PostgresStore{db: db}, nil
}

func (s *PostgresStore) Close() error {
	return s.db.Close()
}

func (s *PostgresStore) Migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS workflows (
	id text PRIMARY KEY,
	name text NOT NULL,
	job_type text NOT NULL,
	payload jsonb,
	metadata jsonb,
	max_attempts integer NOT NULL,
	enabled boolean NOT NULL,
	interval_seconds integer NOT NULL,
	next_run_at timestamptz NOT NULL,
	last_run_at timestamptz,
	last_job_id text NOT NULL DEFAULT '',
	created_at timestamptz NOT NULL,
	updated_at timestamptz NOT NULL
);

CREATE INDEX IF NOT EXISTS workflows_due_idx ON workflows (enabled, next_run_at);
CREATE INDEX IF NOT EXISTS workflows_created_at_idx ON workflows (created_at DESC);
`)
	return err
}

func (s *PostgresStore) Create(params CreateWorkflowParams) (*Workflow, error) {
	now := time.Now().UTC()
	maxAttempts := params.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	nextRunAt := now
	if params.NextRunAt != nil {
		nextRunAt = params.NextRunAt.UTC()
	}

	payload, err := jsonOrNil(params.Payload)
	if err != nil {
		return nil, err
	}
	metadata, err := jsonOrNil(params.Metadata)
	if err != nil {
		return nil, err
	}

	ctx, cancel := s.context()
	defer cancel()

	return scanWorkflow(s.db.QueryRowContext(ctx, `
INSERT INTO workflows (
	id, name, job_type, payload, metadata, max_attempts, enabled, interval_seconds,
	next_run_at, last_job_id, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, '', $10, $10)
RETURNING id, name, job_type, payload, metadata, max_attempts, enabled, interval_seconds,
	next_run_at, last_run_at, last_job_id, created_at, updated_at
`, newID(), params.Name, params.JobType, payload, metadata, maxAttempts, params.Enabled,
		params.IntervalSeconds, nextRunAt, now))
}

func (s *PostgresStore) Get(id string) (*Workflow, error) {
	ctx, cancel := s.context()
	defer cancel()

	workflow, err := scanWorkflow(s.db.QueryRowContext(ctx, `
SELECT id, name, job_type, payload, metadata, max_attempts, enabled, interval_seconds,
	next_run_at, last_run_at, last_job_id, created_at, updated_at
FROM workflows
WHERE id = $1
`, id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return workflow, nil
}

func (s *PostgresStore) List() ([]*Workflow, error) {
	ctx, cancel := s.context()
	defer cancel()

	rows, err := s.db.QueryContext(ctx, `
SELECT id, name, job_type, payload, metadata, max_attempts, enabled, interval_seconds,
	next_run_at, last_run_at, last_job_id, created_at, updated_at
FROM workflows
ORDER BY created_at DESC
`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanWorkflowRows(rows)
}

func (s *PostgresStore) ListDue(now time.Time) ([]*Workflow, error) {
	ctx, cancel := s.context()
	defer cancel()

	rows, err := s.db.QueryContext(ctx, `
SELECT id, name, job_type, payload, metadata, max_attempts, enabled, interval_seconds,
	next_run_at, last_run_at, last_job_id, created_at, updated_at
FROM workflows
WHERE enabled = true AND next_run_at <= $1
ORDER BY next_run_at
`, now.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanWorkflowRows(rows)
}

func (s *PostgresStore) MarkDispatched(id string, jobID string, lastRunAt time.Time, nextRunAt time.Time) (*Workflow, error) {
	now := time.Now().UTC()
	ctx, cancel := s.context()
	defer cancel()

	workflow, err := scanWorkflow(s.db.QueryRowContext(ctx, `
UPDATE workflows
SET last_run_at = $2, last_job_id = $3, next_run_at = $4, updated_at = $5
WHERE id = $1
RETURNING id, name, job_type, payload, metadata, max_attempts, enabled, interval_seconds,
	next_run_at, last_run_at, last_job_id, created_at, updated_at
`, id, lastRunAt.UTC(), jobID, nextRunAt.UTC(), now))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return workflow, nil
}

func (s *PostgresStore) context() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), postgresStoreTimeout)
}

func scanWorkflowRows(rows *sql.Rows) ([]*Workflow, error) {
	var out []*Workflow
	for rows.Next() {
		workflow, err := scanWorkflow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, workflow)
	}
	return out, rows.Err()
}

type workflowScanner interface {
	Scan(dest ...any) error
}

func scanWorkflow(scanner workflowScanner) (*Workflow, error) {
	var workflow Workflow
	var payload []byte
	var metadata []byte
	var lastRunAt sql.NullTime

	err := scanner.Scan(
		&workflow.ID,
		&workflow.Name,
		&workflow.JobType,
		&payload,
		&metadata,
		&workflow.MaxAttempts,
		&workflow.Enabled,
		&workflow.IntervalSeconds,
		&workflow.NextRunAt,
		&lastRunAt,
		&workflow.LastJobID,
		&workflow.CreatedAt,
		&workflow.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &workflow.Payload); err != nil {
			return nil, err
		}
	}
	if len(metadata) > 0 {
		if err := json.Unmarshal(metadata, &workflow.Metadata); err != nil {
			return nil, err
		}
	}
	if lastRunAt.Valid {
		workflow.LastRunAt = &lastRunAt.Time
	}
	return &workflow, nil
}

func jsonOrNil(value any) (any, error) {
	if value == nil {
		return nil, nil
	}

	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if string(data) == "null" {
		return nil, nil
	}
	return data, nil
}
