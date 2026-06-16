package scheduler

import (
	"context"
	"database/sql"
	"hash/fnv"

	_ "github.com/jackc/pgx/v5/stdlib"
)

type PostgresAdvisoryLock struct {
	db  *sql.DB
	key int64
}

func NewPostgresAdvisoryLock(ctx context.Context, databaseURL string, name string) (*PostgresAdvisoryLock, error) {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &PostgresAdvisoryLock{db: db, key: advisoryKey(name)}, nil
}

func (l *PostgresAdvisoryLock) Close() error {
	return l.db.Close()
}

func (l *PostgresAdvisoryLock) TryLock(ctx context.Context) (func(), bool, error) {
	conn, err := l.db.Conn(ctx)
	if err != nil {
		return nil, false, err
	}
	var acquired bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, l.key).Scan(&acquired); err != nil {
		_ = conn.Close()
		return nil, false, err
	}
	if !acquired {
		_ = conn.Close()
		return nil, false, nil
	}
	return func() {
		_, _ = conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, l.key)
		_ = conn.Close()
	}, true, nil
}

func advisoryKey(name string) int64 {
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(name))
	return int64(hash.Sum64())
}
