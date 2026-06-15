package jobs

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
CREATE TABLE IF NOT EXISTS jobs (
	id text PRIMARY KEY,
	name text NOT NULL,
	job_type text NOT NULL,
	status text NOT NULL,
	payload jsonb,
	attempts integer NOT NULL,
	max_attempts integer NOT NULL,
	error text NOT NULL DEFAULT '',
	metadata jsonb,
	created_at timestamptz NOT NULL,
	updated_at timestamptz NOT NULL,
	started_at timestamptz,
	finished_at timestamptz,
	lease_owner text NOT NULL DEFAULT '',
	lease_until timestamptz
);

ALTER TABLE jobs ADD COLUMN IF NOT EXISTS lease_owner text NOT NULL DEFAULT '';
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS lease_until timestamptz;

CREATE INDEX IF NOT EXISTS jobs_status_idx ON jobs (status);
CREATE INDEX IF NOT EXISTS jobs_created_at_idx ON jobs (created_at DESC);

CREATE TABLE IF NOT EXISTS job_logs (
	id bigserial PRIMARY KEY,
	job_id text NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
	time timestamptz NOT NULL,
	message text NOT NULL
);

CREATE INDEX IF NOT EXISTS job_logs_job_id_time_idx ON job_logs (job_id, time);
`)
	return err
}

func (s *PostgresStore) Create(params CreateJobParams) (*Job, error) {
	now := time.Now().UTC()
	maxAttempts := params.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 1
	}

	payload, err := jsonOrNil(params.Payload)
	if err != nil {
		return nil, err
	}
	metadata, err := jsonOrNil(params.Metadata)
	if err != nil {
		return nil, err
	}

	job := &Job{
		ID:          newID(),
		Name:        params.Name,
		Type:        params.Type,
		Status:      StatusQueued,
		Payload:     cloneMapAny(params.Payload),
		MaxAttempts: maxAttempts,
		Metadata:    cloneMapString(params.Metadata),
		CreatedAt:   now,
		UpdatedAt:   now,
		Logs: []LogEntry{{
			Time:    now,
			Message: "job queued",
		}},
	}

	ctx, cancel := s.context()
	defer cancel()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer rollback(tx)

	if _, err := tx.ExecContext(ctx, `
INSERT INTO jobs (
	id, name, job_type, status, payload, attempts, max_attempts, error, metadata, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, 0, $6, '', $7, $8, $8)
`, job.ID, job.Name, job.Type, job.Status, payload, job.MaxAttempts, metadata, now); err != nil {
		return nil, err
	}

	if _, err := tx.ExecContext(ctx, `
INSERT INTO job_logs (job_id, time, message) VALUES ($1, $2, $3)
`, job.ID, now, "job queued"); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return cloneJob(job), nil
}

func (s *PostgresStore) Get(id string) (*Job, error) {
	ctx, cancel := s.context()
	defer cancel()

	job, err := s.get(ctx, s.db, id)
	if err != nil {
		return nil, err
	}
	return cloneJob(job), nil
}

func (s *PostgresStore) List() ([]*Job, error) {
	ctx, cancel := s.context()
	defer cancel()

	rows, err := s.db.QueryContext(ctx, `
SELECT id, name, job_type, status, payload, attempts, max_attempts, error, metadata, created_at, updated_at, started_at, finished_at, lease_owner, lease_until
FROM jobs
ORDER BY created_at DESC
`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*Job
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		logs, err := s.logs(ctx, s.db, job.ID)
		if err != nil {
			return nil, err
		}
		job.Logs = logs
		out = append(out, cloneJob(job))
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) MarkRunning(id string) (*Job, error) {
	return s.MarkRunningWithLease(id, "", time.Time{})
}

func (s *PostgresStore) MarkRunningWithLease(id string, workerID string, leaseUntil time.Time) (*Job, error) {
	now := time.Now().UTC()
	var lease any
	if !leaseUntil.IsZero() {
		lease = leaseUntil.UTC()
	}
	return s.transition(id, "job started", func(ctx context.Context, tx *sql.Tx) (*Job, error) {
		return scanJob(tx.QueryRowContext(ctx, `
UPDATE jobs
SET status = $2, attempts = attempts + 1, updated_at = $3, started_at = $3, finished_at = NULL, error = '', lease_owner = $4, lease_until = $5
WHERE id = $1 AND status = $6
RETURNING id, name, job_type, status, payload, attempts, max_attempts, error, metadata, created_at, updated_at, started_at, finished_at, lease_owner, lease_until
`, id, StatusRunning, now, workerID, lease, StatusQueued))
	})
}

func (s *PostgresStore) MarkSucceeded(id string, message string) (*Job, error) {
	return s.finish(id, StatusSucceeded, "", message)
}

func (s *PostgresStore) MarkQueued(id string, message string) (*Job, error) {
	now := time.Now().UTC()
	return s.transition(id, message, func(ctx context.Context, tx *sql.Tx) (*Job, error) {
		return scanJob(tx.QueryRowContext(ctx, `
UPDATE jobs
SET status = $2, error = '', updated_at = $3, finished_at = NULL, lease_owner = '', lease_until = NULL
WHERE id = $1
RETURNING id, name, job_type, status, payload, attempts, max_attempts, error, metadata, created_at, updated_at, started_at, finished_at, lease_owner, lease_until
`, id, StatusQueued, now))
	})
}

func (s *PostgresStore) MarkFailed(id string, errMessage string) (*Job, error) {
	return s.finish(id, StatusFailed, errMessage, "job failed: "+errMessage)
}

func (s *PostgresStore) MarkDeadLetter(id string, errMessage string) (*Job, error) {
	return s.finish(id, StatusDeadLetter, errMessage, "job moved to dead letter: "+errMessage)
}

func (s *PostgresStore) MarkCanceled(id string, message string) (*Job, error) {
	now := time.Now().UTC()
	return s.transition(id, message, func(ctx context.Context, tx *sql.Tx) (*Job, error) {
		return scanJob(tx.QueryRowContext(ctx, `
UPDATE jobs
SET status = $2, error = '', updated_at = $3, finished_at = $3, lease_owner = '', lease_until = NULL
WHERE id = $1 AND status = $4
RETURNING id, name, job_type, status, payload, attempts, max_attempts, error, metadata, created_at, updated_at, started_at, finished_at, lease_owner, lease_until
`, id, StatusCanceled, now, StatusQueued))
	})
}

func (s *PostgresStore) RequeueExpiredLeases(now time.Time) ([]*Job, error) {
	now = now.UTC()
	ctx, cancel := s.context()
	defer cancel()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer rollback(tx)

	rows, err := tx.QueryContext(ctx, `
UPDATE jobs
SET status = CASE WHEN attempts >= max_attempts THEN $2 ELSE $3 END,
	error = CASE WHEN attempts >= max_attempts THEN 'worker lease expired' ELSE '' END,
	updated_at = $4,
	finished_at = CASE WHEN attempts >= max_attempts THEN $4 ELSE NULL END,
	lease_owner = '',
	lease_until = NULL
WHERE status = $1 AND lease_until IS NOT NULL AND lease_until <= $4
RETURNING id, name, job_type, status, payload, attempts, max_attempts, error, metadata, created_at, updated_at, started_at, finished_at, lease_owner, lease_until
`, StatusRunning, StatusDeadLetter, StatusQueued, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*Job
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		message := "worker lease expired; job requeued"
		if job.Status == StatusDeadLetter {
			message = "job moved to dead letter: worker lease expired"
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO job_logs (job_id, time, message) VALUES ($1, $2, $3)
`, job.ID, now, message); err != nil {
			return nil, err
		}
		out = append(out, cloneJob(job))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) AppendLog(id string, message string) error {
	now := time.Now().UTC()
	ctx, cancel := s.context()
	defer cancel()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)

	result, err := tx.ExecContext(ctx, `
UPDATE jobs SET updated_at = $2 WHERE id = $1
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

	if _, err := tx.ExecContext(ctx, `
INSERT INTO job_logs (job_id, time, message) VALUES ($1, $2, $3)
`, id, now, message); err != nil {
		return err
	}

	return tx.Commit()
}

func (s *PostgresStore) finish(id string, status Status, errMessage string, logMessage string) (*Job, error) {
	now := time.Now().UTC()
	return s.transition(id, logMessage, func(ctx context.Context, tx *sql.Tx) (*Job, error) {
		return scanJob(tx.QueryRowContext(ctx, `
UPDATE jobs
SET status = $2, error = $3, updated_at = $4, finished_at = $4, lease_owner = '', lease_until = NULL
WHERE id = $1
RETURNING id, name, job_type, status, payload, attempts, max_attempts, error, metadata, created_at, updated_at, started_at, finished_at, lease_owner, lease_until
`, id, status, errMessage, now))
	})
}

func (s *PostgresStore) transition(id string, logMessage string, update func(context.Context, *sql.Tx) (*Job, error)) (*Job, error) {
	now := time.Now().UTC()
	ctx, cancel := s.context()
	defer cancel()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer rollback(tx)

	job, err := update(ctx, tx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	if _, err := tx.ExecContext(ctx, `
INSERT INTO job_logs (job_id, time, message) VALUES ($1, $2, $3)
`, id, now, logMessage); err != nil {
		return nil, err
	}

	job.Logs, err = s.logs(ctx, tx, id)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return cloneJob(job), nil
}

func (s *PostgresStore) get(ctx context.Context, q queryer, id string) (*Job, error) {
	job, err := scanJob(q.QueryRowContext(ctx, `
SELECT id, name, job_type, status, payload, attempts, max_attempts, error, metadata, created_at, updated_at, started_at, finished_at, lease_owner, lease_until
FROM jobs
WHERE id = $1
`, id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	job.Logs, err = s.logs(ctx, q, id)
	if err != nil {
		return nil, err
	}
	return job, nil
}

func (s *PostgresStore) logs(ctx context.Context, q queryer, id string) ([]LogEntry, error) {
	rows, err := q.QueryContext(ctx, `
SELECT time, message
FROM job_logs
WHERE job_id = $1
ORDER BY time, id
`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []LogEntry
	for rows.Next() {
		var log LogEntry
		if err := rows.Scan(&log.Time, &log.Message); err != nil {
			return nil, err
		}
		logs = append(logs, log)
	}
	return logs, rows.Err()
}

func (s *PostgresStore) context() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), postgresStoreTimeout)
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type jobScanner interface {
	Scan(dest ...any) error
}

func scanJob(scanner jobScanner) (*Job, error) {
	var job Job
	var status string
	var payload []byte
	var metadata []byte
	var startedAt sql.NullTime
	var finishedAt sql.NullTime
	var leaseUntil sql.NullTime

	err := scanner.Scan(
		&job.ID,
		&job.Name,
		&job.Type,
		&status,
		&payload,
		&job.Attempts,
		&job.MaxAttempts,
		&job.Error,
		&metadata,
		&job.CreatedAt,
		&job.UpdatedAt,
		&startedAt,
		&finishedAt,
		&job.LeaseOwner,
		&leaseUntil,
	)
	if err != nil {
		return nil, err
	}

	job.Status = Status(status)
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &job.Payload); err != nil {
			return nil, err
		}
	}
	if len(metadata) > 0 {
		if err := json.Unmarshal(metadata, &job.Metadata); err != nil {
			return nil, err
		}
	}
	if startedAt.Valid {
		job.StartedAt = &startedAt.Time
	}
	if finishedAt.Valid {
		job.FinishedAt = &finishedAt.Time
	}
	if leaseUntil.Valid {
		job.LeaseUntil = &leaseUntil.Time
	}

	return &job, nil
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

func rollback(tx *sql.Tx) {
	_ = tx.Rollback()
}
