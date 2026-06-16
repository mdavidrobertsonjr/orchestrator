package workflowruns

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"orchestrator/backend/internal/database"

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
	return database.RunMigrations(ctx, s.db, "workflowruns", []database.Migration{{
		Version: 1,
		Name:    "create_workflow_runs",
		SQL: `
CREATE TABLE IF NOT EXISTS workflow_runs (
	id text PRIMARY KEY,
	workflow_id text NOT NULL,
	job_id text NOT NULL,
	trigger text NOT NULL,
	status text NOT NULL,
	scheduled_for timestamptz,
	idempotency_key text NOT NULL DEFAULT '',
	metadata jsonb,
	created_at timestamptz NOT NULL,
	updated_at timestamptz NOT NULL
);

ALTER TABLE workflow_runs ADD COLUMN IF NOT EXISTS scheduled_for timestamptz;
ALTER TABLE workflow_runs ADD COLUMN IF NOT EXISTS idempotency_key text NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS workflow_runs_created_at_idx ON workflow_runs (created_at DESC);
CREATE INDEX IF NOT EXISTS workflow_runs_workflow_id_idx ON workflow_runs (workflow_id);
CREATE INDEX IF NOT EXISTS workflow_runs_job_id_idx ON workflow_runs (job_id);
CREATE UNIQUE INDEX IF NOT EXISTS workflow_runs_idempotency_key_idx ON workflow_runs (idempotency_key) WHERE idempotency_key <> '';
`,
	}})
}

func (s *PostgresStore) Create(params CreateRunParams) (*Run, error) {
	now := time.Now().UTC()
	status := params.Status
	if status == "" {
		status = "queued"
	}
	metadata, err := jsonOrNil(params.Metadata)
	if err != nil {
		return nil, err
	}

	ctx, cancel := s.context()
	defer cancel()

	return scanRun(s.db.QueryRowContext(ctx, `
INSERT INTO workflow_runs (
	id, workflow_id, job_id, trigger, status, scheduled_for, idempotency_key, metadata, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9)
RETURNING id, workflow_id, job_id, trigger, status, scheduled_for, idempotency_key, metadata, created_at, updated_at
`, newID(), params.WorkflowID, params.JobID, params.Trigger, status, params.ScheduledFor, params.IdempotencyKey, metadata, now))
}

func (s *PostgresStore) Get(id string) (*Run, error) {
	ctx, cancel := s.context()
	defer cancel()

	run, err := scanRun(s.db.QueryRowContext(ctx, `
SELECT id, workflow_id, job_id, trigger, status, scheduled_for, idempotency_key, metadata, created_at, updated_at
FROM workflow_runs
WHERE id = $1
`, id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return run, nil
}

func (s *PostgresStore) GetByIdempotencyKey(key string) (*Run, error) {
	ctx, cancel := s.context()
	defer cancel()

	run, err := scanRun(s.db.QueryRowContext(ctx, `
SELECT id, workflow_id, job_id, trigger, status, scheduled_for, idempotency_key, metadata, created_at, updated_at
FROM workflow_runs
WHERE idempotency_key = $1
`, key))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return run, nil
}

func (s *PostgresStore) List() ([]*Run, error) {
	ctx, cancel := s.context()
	defer cancel()

	rows, err := s.db.QueryContext(ctx, `
SELECT id, workflow_id, job_id, trigger, status, scheduled_for, idempotency_key, metadata, created_at, updated_at
FROM workflow_runs
ORDER BY created_at DESC
`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRunRows(rows)
}

func (s *PostgresStore) ListByWorkflow(workflowID string) ([]*Run, error) {
	ctx, cancel := s.context()
	defer cancel()

	rows, err := s.db.QueryContext(ctx, `
SELECT id, workflow_id, job_id, trigger, status, scheduled_for, idempotency_key, metadata, created_at, updated_at
FROM workflow_runs
WHERE workflow_id = $1
ORDER BY created_at DESC
`, workflowID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRunRows(rows)
}

func (s *PostgresStore) ListPage(params ListParams) ([]*Run, int, error) {
	ctx, cancel := s.context()
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `
SELECT id, workflow_id, job_id, trigger, status, scheduled_for, idempotency_key, metadata, created_at, updated_at
FROM workflow_runs
WHERE ($1 = '' OR workflow_id = $1)
	AND ($2 = '' OR job_id = $2)
	AND ($3 = '' OR lower(status) = $3)
	AND ($4 = '' OR lower(trigger) = $4)
ORDER BY created_at DESC
LIMIT NULLIF($5, 0) OFFSET $6
`, params.WorkflowID, params.JobID, strings.ToLower(params.Status), strings.ToLower(params.Trigger), params.Limit, params.Offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out, err := scanRunRows(rows)
	if err != nil {
		return nil, 0, err
	}
	var total int
	if err := s.db.QueryRowContext(ctx, `
SELECT count(*)
FROM workflow_runs
WHERE ($1 = '' OR workflow_id = $1)
	AND ($2 = '' OR job_id = $2)
	AND ($3 = '' OR lower(status) = $3)
	AND ($4 = '' OR lower(trigger) = $4)
`, params.WorkflowID, params.JobID, strings.ToLower(params.Status), strings.ToLower(params.Trigger)).Scan(&total); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

func (s *PostgresStore) MarkStatusByJob(jobID string, status string) (*Run, error) {
	now := time.Now().UTC()
	ctx, cancel := s.context()
	defer cancel()

	run, err := scanRun(s.db.QueryRowContext(ctx, `
UPDATE workflow_runs
SET status = $2, updated_at = $3
WHERE job_id = $1
RETURNING id, workflow_id, job_id, trigger, status, scheduled_for, idempotency_key, metadata, created_at, updated_at
`, jobID, status, now))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return run, nil
}

func (s *PostgresStore) context() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), postgresStoreTimeout)
}

func scanRunRows(rows *sql.Rows) ([]*Run, error) {
	var out []*Run
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

type runScanner interface {
	Scan(dest ...any) error
}

func scanRun(scanner runScanner) (*Run, error) {
	var run Run
	var metadata []byte
	var scheduledFor sql.NullTime
	err := scanner.Scan(
		&run.ID,
		&run.WorkflowID,
		&run.JobID,
		&run.Trigger,
		&run.Status,
		&scheduledFor,
		&run.IdempotencyKey,
		&metadata,
		&run.CreatedAt,
		&run.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	if len(metadata) > 0 {
		if err := json.Unmarshal(metadata, &run.Metadata); err != nil {
			return nil, err
		}
	}
	if scheduledFor.Valid {
		run.ScheduledFor = &scheduledFor.Time
	}
	return &run, nil
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
