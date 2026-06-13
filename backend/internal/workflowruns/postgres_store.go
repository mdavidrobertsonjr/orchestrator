package workflowruns

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
CREATE TABLE IF NOT EXISTS workflow_runs (
	id text PRIMARY KEY,
	workflow_id text NOT NULL,
	job_id text NOT NULL,
	trigger text NOT NULL,
	status text NOT NULL,
	metadata jsonb,
	created_at timestamptz NOT NULL,
	updated_at timestamptz NOT NULL
);

CREATE INDEX IF NOT EXISTS workflow_runs_created_at_idx ON workflow_runs (created_at DESC);
CREATE INDEX IF NOT EXISTS workflow_runs_workflow_id_idx ON workflow_runs (workflow_id);
CREATE INDEX IF NOT EXISTS workflow_runs_job_id_idx ON workflow_runs (job_id);
`)
	return err
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
	id, workflow_id, job_id, trigger, status, metadata, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $7)
RETURNING id, workflow_id, job_id, trigger, status, metadata, created_at, updated_at
`, newID(), params.WorkflowID, params.JobID, params.Trigger, status, metadata, now))
}

func (s *PostgresStore) Get(id string) (*Run, error) {
	ctx, cancel := s.context()
	defer cancel()

	run, err := scanRun(s.db.QueryRowContext(ctx, `
SELECT id, workflow_id, job_id, trigger, status, metadata, created_at, updated_at
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

func (s *PostgresStore) List() ([]*Run, error) {
	ctx, cancel := s.context()
	defer cancel()

	rows, err := s.db.QueryContext(ctx, `
SELECT id, workflow_id, job_id, trigger, status, metadata, created_at, updated_at
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
SELECT id, workflow_id, job_id, trigger, status, metadata, created_at, updated_at
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
	err := scanner.Scan(
		&run.ID,
		&run.WorkflowID,
		&run.JobID,
		&run.Trigger,
		&run.Status,
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
