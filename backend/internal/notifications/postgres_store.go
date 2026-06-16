package notifications

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
	return database.RunMigrations(ctx, s.db, "notifications", []database.Migration{{
		Version: 1,
		Name:    "create_notification_deliveries",
		SQL: `
CREATE TABLE IF NOT EXISTS notification_deliveries (
	id text PRIMARY KEY,
	job_id text NOT NULL DEFAULT '',
	workflow_id text NOT NULL DEFAULT '',
	kind text NOT NULL,
	provider text NOT NULL,
	status text NOT NULL,
	recipients jsonb,
	subject text NOT NULL DEFAULT '',
	error text NOT NULL DEFAULT '',
	attempts integer NOT NULL DEFAULT 0,
	last_attempt_at timestamptz,
	metadata jsonb,
	created_at timestamptz NOT NULL,
	updated_at timestamptz NOT NULL
);

CREATE INDEX IF NOT EXISTS notification_deliveries_created_at_idx ON notification_deliveries (created_at DESC);
CREATE INDEX IF NOT EXISTS notification_deliveries_status_idx ON notification_deliveries (status);
CREATE INDEX IF NOT EXISTS notification_deliveries_workflow_id_idx ON notification_deliveries (workflow_id);
`,
	}})
}

func (s *PostgresStore) Create(params CreateDeliveryParams) (*Delivery, error) {
	now := time.Now().UTC()
	recipients, err := jsonOrNil(params.Recipients)
	if err != nil {
		return nil, err
	}
	metadata, err := jsonOrNil(params.Metadata)
	if err != nil {
		return nil, err
	}
	ctx, cancel := s.context()
	defer cancel()
	return scanDelivery(s.db.QueryRowContext(ctx, `
INSERT INTO notification_deliveries (
	id, job_id, workflow_id, kind, provider, status, recipients, subject, metadata, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, 'pending', $6, $7, $8, $9, $9)
RETURNING id, job_id, workflow_id, kind, provider, status, recipients, subject, error, attempts, last_attempt_at, metadata, created_at, updated_at
`, newID(), params.JobID, params.WorkflowID, params.Kind, params.Provider, recipients, params.Subject, metadata, now))
}

func (s *PostgresStore) MarkAttempt(id string) (*Delivery, error) {
	now := time.Now().UTC()
	ctx, cancel := s.context()
	defer cancel()
	return s.scanUpdated(s.db.QueryRowContext(ctx, `
UPDATE notification_deliveries
SET status = 'sending', attempts = attempts + 1, error = '', last_attempt_at = $2, updated_at = $2
WHERE id = $1
RETURNING id, job_id, workflow_id, kind, provider, status, recipients, subject, error, attempts, last_attempt_at, metadata, created_at, updated_at
`, id, now))
}

func (s *PostgresStore) MarkSucceeded(id string) (*Delivery, error) {
	return s.setStatus(id, "succeeded", "")
}

func (s *PostgresStore) MarkFailed(id string, message string) (*Delivery, error) {
	return s.setStatus(id, "failed", message)
}

func (s *PostgresStore) setStatus(id string, status string, message string) (*Delivery, error) {
	now := time.Now().UTC()
	ctx, cancel := s.context()
	defer cancel()
	return s.scanUpdated(s.db.QueryRowContext(ctx, `
UPDATE notification_deliveries
SET status = $2, error = $3, updated_at = $4
WHERE id = $1
RETURNING id, job_id, workflow_id, kind, provider, status, recipients, subject, error, attempts, last_attempt_at, metadata, created_at, updated_at
`, id, status, message, now))
}

func (s *PostgresStore) List() ([]*Delivery, error) {
	ctx, cancel := s.context()
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `
SELECT id, job_id, workflow_id, kind, provider, status, recipients, subject, error, attempts, last_attempt_at, metadata, created_at, updated_at
FROM notification_deliveries
ORDER BY created_at DESC
`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Delivery
	for rows.Next() {
		delivery, err := scanDelivery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, delivery)
	}
	return out, rows.Err()
}

func (s *PostgresStore) scanUpdated(row deliveryScanner) (*Delivery, error) {
	delivery, err := scanDelivery(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return delivery, nil
}

func (s *PostgresStore) context() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), postgresStoreTimeout)
}

type deliveryScanner interface {
	Scan(dest ...any) error
}

func scanDelivery(scanner deliveryScanner) (*Delivery, error) {
	var delivery Delivery
	var recipients []byte
	var metadata []byte
	var lastAttempt sql.NullTime
	if err := scanner.Scan(
		&delivery.ID,
		&delivery.JobID,
		&delivery.WorkflowID,
		&delivery.Kind,
		&delivery.Provider,
		&delivery.Status,
		&recipients,
		&delivery.Subject,
		&delivery.Error,
		&delivery.Attempts,
		&lastAttempt,
		&metadata,
		&delivery.CreatedAt,
		&delivery.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if len(recipients) > 0 {
		if err := json.Unmarshal(recipients, &delivery.Recipients); err != nil {
			return nil, err
		}
	}
	if len(metadata) > 0 {
		if err := json.Unmarshal(metadata, &delivery.Metadata); err != nil {
			return nil, err
		}
	}
	if lastAttempt.Valid {
		delivery.LastAttempt = &lastAttempt.Time
	}
	return &delivery, nil
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
