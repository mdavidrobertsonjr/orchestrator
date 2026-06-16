package workers

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"orchestrator/backend/internal/database"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const postgresRegistryTimeout = 5 * time.Second

type PostgresRegistry struct {
	db *sql.DB
}

func NewPostgresRegistry(ctx context.Context, databaseURL string) (*PostgresRegistry, error) {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &PostgresRegistry{db: db}, nil
}

func (r *PostgresRegistry) Close() error {
	return r.db.Close()
}

func (r *PostgresRegistry) Migrate(ctx context.Context) error {
	return database.RunMigrations(ctx, r.db, "workers", []database.Migration{{
		Version: 1,
		Name:    "create_workers",
		SQL: `
CREATE TABLE IF NOT EXISTS workers (
	id text PRIMARY KEY,
	status text NOT NULL,
	current_job_id text NOT NULL DEFAULT '',
	last_heartbeat timestamptz NOT NULL,
	started_at timestamptz NOT NULL,
	updated_at timestamptz NOT NULL
);

CREATE INDEX IF NOT EXISTS workers_status_idx ON workers (status);
CREATE INDEX IF NOT EXISTS workers_updated_at_idx ON workers (updated_at DESC);
`,
	}})
}

func (r *PostgresRegistry) Register(id string) (*Worker, error) {
	now := time.Now().UTC()
	ctx, cancel := r.context()
	defer cancel()

	return scanWorker(r.db.QueryRowContext(ctx, `
INSERT INTO workers (id, status, current_job_id, last_heartbeat, started_at, updated_at)
VALUES ($1, $2, '', $3, $3, $3)
ON CONFLICT (id) DO UPDATE
SET status = $2, current_job_id = '', last_heartbeat = $3, updated_at = $3
RETURNING id, status, current_job_id, last_heartbeat, started_at, updated_at
`, id, StatusIdle, now))
}

func (r *PostgresRegistry) Heartbeat(id string) error {
	now := time.Now().UTC()
	ctx, cancel := r.context()
	defer cancel()

	result, err := r.db.ExecContext(ctx, `
UPDATE workers SET last_heartbeat = $2, updated_at = $2 WHERE id = $1
`, id, now)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *PostgresRegistry) MarkRunning(id string, jobID string) (*Worker, error) {
	return r.update(id, StatusRunning, jobID)
}

func (r *PostgresRegistry) MarkIdle(id string) (*Worker, error) {
	return r.update(id, StatusIdle, "")
}

func (r *PostgresRegistry) MarkStopped(id string) (*Worker, error) {
	return r.update(id, StatusStopped, "")
}

func (r *PostgresRegistry) Get(id string) (*Worker, error) {
	ctx, cancel := r.context()
	defer cancel()

	return scanWorker(r.db.QueryRowContext(ctx, `
SELECT id, status, current_job_id, last_heartbeat, started_at, updated_at
FROM workers
WHERE id = $1
`, id))
}

func (r *PostgresRegistry) List() []*Worker {
	ctx, cancel := r.context()
	defer cancel()

	rows, err := r.db.QueryContext(ctx, `
SELECT id, status, current_job_id, last_heartbeat, started_at, updated_at
FROM workers
ORDER BY id ASC
`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var out []*Worker
	for rows.Next() {
		worker, err := scanWorker(rows)
		if err != nil {
			return nil
		}
		out = append(out, worker)
	}
	if rows.Err() != nil {
		return nil
	}
	return out
}

func (r *PostgresRegistry) update(id string, status Status, currentJobID string) (*Worker, error) {
	now := time.Now().UTC()
	ctx, cancel := r.context()
	defer cancel()

	return scanWorker(r.db.QueryRowContext(ctx, `
UPDATE workers
SET status = $2, current_job_id = $3, last_heartbeat = $4, updated_at = $4
WHERE id = $1
RETURNING id, status, current_job_id, last_heartbeat, started_at, updated_at
`, id, status, currentJobID, now))
}

func (r *PostgresRegistry) context() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), postgresRegistryTimeout)
}

type workerScanner interface {
	Scan(dest ...any) error
}

func scanWorker(scanner workerScanner) (*Worker, error) {
	var worker Worker
	if err := scanner.Scan(
		&worker.ID,
		&worker.Status,
		&worker.CurrentJobID,
		&worker.LastHeartbeat,
		&worker.StartedAt,
		&worker.UpdatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &worker, nil
}
