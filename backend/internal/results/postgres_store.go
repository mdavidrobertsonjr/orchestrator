package results

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
	return database.RunMigrations(ctx, s.db, "results", []database.Migration{{
		Version: 1,
		Name:    "create_results",
		SQL: `
CREATE TABLE IF NOT EXISTS results (
	id text PRIMARY KEY,
	job_id text NOT NULL DEFAULT '',
	workflow_id text NOT NULL DEFAULT '',
	result_type text NOT NULL,
	summary text NOT NULL DEFAULT '',
	data jsonb,
	created_at timestamptz NOT NULL,
	updated_at timestamptz NOT NULL
);

CREATE INDEX IF NOT EXISTS results_created_at_idx ON results (created_at DESC);
CREATE INDEX IF NOT EXISTS results_job_id_idx ON results (job_id);
CREATE INDEX IF NOT EXISTS results_workflow_id_idx ON results (workflow_id);
CREATE INDEX IF NOT EXISTS results_type_idx ON results (result_type);
`,
	}})
}

func (s *PostgresStore) Create(params CreateResultParams) (*Result, error) {
	now := time.Now().UTC()
	data, err := jsonOrNil(params.Data)
	if err != nil {
		return nil, err
	}

	ctx, cancel := s.context()
	defer cancel()

	return scanResult(s.db.QueryRowContext(ctx, `
INSERT INTO results (
	id, job_id, workflow_id, result_type, summary, data, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $7)
RETURNING id, job_id, workflow_id, result_type, summary, data, created_at, updated_at
`, newID(), params.JobID, params.WorkflowID, params.Type, params.Summary, data, now))
}

func (s *PostgresStore) Get(id string) (*Result, error) {
	ctx, cancel := s.context()
	defer cancel()

	result, err := scanResult(s.db.QueryRowContext(ctx, `
SELECT id, job_id, workflow_id, result_type, summary, data, created_at, updated_at
FROM results
WHERE id = $1
`, id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return result, nil
}

func (s *PostgresStore) List() ([]*Result, error) {
	ctx, cancel := s.context()
	defer cancel()

	rows, err := s.db.QueryContext(ctx, `
SELECT id, job_id, workflow_id, result_type, summary, data, created_at, updated_at
FROM results
ORDER BY created_at DESC
`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanResultRows(rows)
}

func (s *PostgresStore) ListByJob(jobID string) ([]*Result, error) {
	ctx, cancel := s.context()
	defer cancel()

	rows, err := s.db.QueryContext(ctx, `
SELECT id, job_id, workflow_id, result_type, summary, data, created_at, updated_at
FROM results
WHERE job_id = $1
ORDER BY created_at DESC
`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanResultRows(rows)
}

func (s *PostgresStore) ListByWorkflow(workflowID string) ([]*Result, error) {
	ctx, cancel := s.context()
	defer cancel()

	rows, err := s.db.QueryContext(ctx, `
SELECT id, job_id, workflow_id, result_type, summary, data, created_at, updated_at
FROM results
WHERE workflow_id = $1
ORDER BY created_at DESC
`, workflowID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanResultRows(rows)
}

func (s *PostgresStore) ListPage(params ListParams) ([]*Result, int, error) {
	ctx, cancel := s.context()
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `
SELECT id, job_id, workflow_id, result_type, summary, data, created_at, updated_at
FROM results
WHERE ($1 = '' OR job_id = $1)
	AND ($2 = '' OR workflow_id = $2)
	AND ($3 = '' OR lower(result_type) = $3)
	AND ($4 = '' OR lower(id || ' ' || result_type || ' ' || summary) LIKE '%' || $4 || '%')
ORDER BY created_at DESC
LIMIT NULLIF($5, 0) OFFSET $6
`, params.JobID, params.WorkflowID, strings.ToLower(params.Type), strings.ToLower(params.Query), params.Limit, params.Offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out, err := scanResultRows(rows)
	if err != nil {
		return nil, 0, err
	}
	var total int
	if err := s.db.QueryRowContext(ctx, `
SELECT count(*)
FROM results
WHERE ($1 = '' OR job_id = $1)
	AND ($2 = '' OR workflow_id = $2)
	AND ($3 = '' OR lower(result_type) = $3)
	AND ($4 = '' OR lower(id || ' ' || result_type || ' ' || summary) LIKE '%' || $4 || '%')
`, params.JobID, params.WorkflowID, strings.ToLower(params.Type), strings.ToLower(params.Query)).Scan(&total); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

func (s *PostgresStore) context() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), postgresStoreTimeout)
}

func scanResultRows(rows *sql.Rows) ([]*Result, error) {
	var out []*Result
	for rows.Next() {
		result, err := scanResult(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, result)
	}
	return out, rows.Err()
}

type resultScanner interface {
	Scan(dest ...any) error
}

func scanResult(scanner resultScanner) (*Result, error) {
	var result Result
	var data []byte

	err := scanner.Scan(
		&result.ID,
		&result.JobID,
		&result.WorkflowID,
		&result.Type,
		&result.Summary,
		&data,
		&result.CreatedAt,
		&result.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	if len(data) > 0 {
		if err := json.Unmarshal(data, &result.Data); err != nil {
			return nil, err
		}
	}
	return &result, nil
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
