package postings

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
	return database.RunMigrations(ctx, s.db, "postings", []database.Migration{{
		Version: 1,
		Name:    "create_postings",
		SQL: `
CREATE TABLE IF NOT EXISTS postings (
	id text PRIMARY KEY,
	company text NOT NULL,
	title text NOT NULL,
	url text NOT NULL,
	location text NOT NULL DEFAULT '',
	source text NOT NULL,
	source_id text NOT NULL DEFAULT '',
	dedupe_key text NOT NULL UNIQUE,
	posted_at timestamptz,
	first_seen_at timestamptz NOT NULL,
	last_seen_at timestamptz NOT NULL,
	matched_at timestamptz,
	match_score integer NOT NULL DEFAULT 0,
	match_reasons jsonb,
	metadata jsonb
);

CREATE INDEX IF NOT EXISTS postings_first_seen_at_idx ON postings (first_seen_at DESC);
CREATE INDEX IF NOT EXISTS postings_last_seen_at_idx ON postings (last_seen_at DESC);
CREATE INDEX IF NOT EXISTS postings_source_idx ON postings (source);
`,
	}})
}

func (s *PostgresStore) Upsert(params UpsertPostingParams) (*Posting, bool, error) {
	now := time.Now().UTC()
	params.Company = stringsTrim(params.Company)
	params.Title = stringsTrim(params.Title)
	params.URL = stringsTrim(params.URL)
	params.Location = stringsTrim(params.Location)
	params.Source = stringsTrim(params.Source)
	params.SourceID = stringsTrim(params.SourceID)
	dedupeKey := dedupeKey(params)

	matchReasons, err := jsonOrNil(params.MatchReasons)
	if err != nil {
		return nil, false, err
	}
	metadata, err := jsonOrNil(params.Metadata)
	if err != nil {
		return nil, false, err
	}

	ctx, cancel := s.context()
	defer cancel()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer rollback(tx)

	var existingID string
	err = tx.QueryRowContext(ctx, `
SELECT id FROM postings WHERE dedupe_key = $1
`, dedupeKey).Scan(&existingID)
	if err == nil {
		posting, err := scanPosting(tx.QueryRowContext(ctx, `
UPDATE postings
SET company = $2, title = $3, url = $4, location = $5, source = $6, source_id = $7,
	posted_at = $8, last_seen_at = $9, matched_at = $10, match_score = $11,
	match_reasons = $12, metadata = $13
WHERE id = $1
RETURNING id, company, title, url, location, source, source_id, dedupe_key, posted_at,
	first_seen_at, last_seen_at, matched_at, match_score, match_reasons, metadata
`, existingID, params.Company, params.Title, params.URL, params.Location, params.Source, params.SourceID,
			params.PostedAt, now, params.MatchedAt, params.MatchScore, matchReasons, metadata))
		if err != nil {
			return nil, false, err
		}
		if err := tx.Commit(); err != nil {
			return nil, false, err
		}
		return posting, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}

	posting, err := scanPosting(tx.QueryRowContext(ctx, `
INSERT INTO postings (
	id, company, title, url, location, source, source_id, dedupe_key, posted_at,
	first_seen_at, last_seen_at, matched_at, match_score, match_reasons, metadata
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $10, $11, $12, $13, $14)
RETURNING id, company, title, url, location, source, source_id, dedupe_key, posted_at,
	first_seen_at, last_seen_at, matched_at, match_score, match_reasons, metadata
`, newID(), params.Company, params.Title, params.URL, params.Location, params.Source, params.SourceID,
		dedupeKey, params.PostedAt, now, params.MatchedAt, params.MatchScore, matchReasons, metadata))
	if err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return posting, true, nil
}

func (s *PostgresStore) Get(id string) (*Posting, error) {
	ctx, cancel := s.context()
	defer cancel()

	posting, err := scanPosting(s.db.QueryRowContext(ctx, `
SELECT id, company, title, url, location, source, source_id, dedupe_key, posted_at,
	first_seen_at, last_seen_at, matched_at, match_score, match_reasons, metadata
FROM postings
WHERE id = $1
`, id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return posting, nil
}

func (s *PostgresStore) List() ([]*Posting, error) {
	ctx, cancel := s.context()
	defer cancel()

	rows, err := s.db.QueryContext(ctx, `
SELECT id, company, title, url, location, source, source_id, dedupe_key, posted_at,
	first_seen_at, last_seen_at, matched_at, match_score, match_reasons, metadata
FROM postings
ORDER BY first_seen_at DESC
`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*Posting
	for rows.Next() {
		posting, err := scanPosting(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, posting)
	}
	return out, rows.Err()
}

func (s *PostgresStore) ListPage(params ListParams) ([]*Posting, int, error) {
	ctx, cancel := s.context()
	defer cancel()

	rows, err := s.db.QueryContext(ctx, `
SELECT id, company, title, url, location, source, source_id, dedupe_key, posted_at,
	first_seen_at, last_seen_at, matched_at, match_score, match_reasons, metadata
FROM postings
WHERE ($1 = '' OR lower(company) LIKE '%' || $1 || '%')
	AND ($2 = '' OR lower(source) = $2)
	AND ($3 = '' OR lower(location) LIKE '%' || $3 || '%')
	AND ($4 = 0 OR match_score >= $4)
	AND ($5 = '' OR lower(company || ' ' || title || ' ' || location || ' ' || url) LIKE '%' || $5 || '%')
ORDER BY first_seen_at DESC
LIMIT NULLIF($6, 0) OFFSET $7
`, strings.ToLower(params.Company), strings.ToLower(params.Source), strings.ToLower(params.Location),
		params.MinScore, strings.ToLower(params.Query), params.Limit, params.Offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var out []*Posting
	for rows.Next() {
		posting, err := scanPosting(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, posting)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var total int
	if err := s.db.QueryRowContext(ctx, `
SELECT count(*)
FROM postings
WHERE ($1 = '' OR lower(company) LIKE '%' || $1 || '%')
	AND ($2 = '' OR lower(source) = $2)
	AND ($3 = '' OR lower(location) LIKE '%' || $3 || '%')
	AND ($4 = 0 OR match_score >= $4)
	AND ($5 = '' OR lower(company || ' ' || title || ' ' || location || ' ' || url) LIKE '%' || $5 || '%')
`, strings.ToLower(params.Company), strings.ToLower(params.Source), strings.ToLower(params.Location),
		params.MinScore, strings.ToLower(params.Query)).Scan(&total); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

func (s *PostgresStore) context() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), postgresStoreTimeout)
}

type postingScanner interface {
	Scan(dest ...any) error
}

func scanPosting(scanner postingScanner) (*Posting, error) {
	var posting Posting
	var postedAt sql.NullTime
	var matchedAt sql.NullTime
	var matchReasons []byte
	var metadata []byte

	err := scanner.Scan(
		&posting.ID,
		&posting.Company,
		&posting.Title,
		&posting.URL,
		&posting.Location,
		&posting.Source,
		&posting.SourceID,
		&posting.DedupeKey,
		&postedAt,
		&posting.FirstSeenAt,
		&posting.LastSeenAt,
		&matchedAt,
		&posting.MatchScore,
		&matchReasons,
		&metadata,
	)
	if err != nil {
		return nil, err
	}

	if postedAt.Valid {
		posting.PostedAt = &postedAt.Time
	}
	if matchedAt.Valid {
		posting.MatchedAt = &matchedAt.Time
	}
	if len(matchReasons) > 0 {
		if err := json.Unmarshal(matchReasons, &posting.MatchReasons); err != nil {
			return nil, err
		}
	}
	if len(metadata) > 0 {
		if err := json.Unmarshal(metadata, &posting.Metadata); err != nil {
			return nil, err
		}
	}
	return &posting, nil
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

func stringsTrim(value string) string {
	return strings.TrimSpace(value)
}
