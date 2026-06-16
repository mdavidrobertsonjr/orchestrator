package database

import (
	"context"
	"database/sql"
	"fmt"
)

type Migration struct {
	Version int
	Name    string
	SQL     string
}

func RunMigrations(ctx context.Context, db *sql.DB, namespace string, migrations []Migration) error {
	if _, err := db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS schema_migrations (
	namespace text NOT NULL,
	version integer NOT NULL,
	name text NOT NULL,
	applied_at timestamptz NOT NULL DEFAULT now(),
	PRIMARY KEY (namespace, version)
);
`); err != nil {
		return err
	}

	for _, migration := range migrations {
		if migration.Version <= 0 {
			return fmt.Errorf("invalid migration version %d for %s", migration.Version, namespace)
		}
		if err := applyMigration(ctx, db, namespace, migration); err != nil {
			return err
		}
	}
	return nil
}

func applyMigration(ctx context.Context, db *sql.DB, namespace string, migration Migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	var applied bool
	if err := tx.QueryRowContext(ctx, `
SELECT EXISTS (
	SELECT 1 FROM schema_migrations WHERE namespace = $1 AND version = $2
)
`, namespace, migration.Version).Scan(&applied); err != nil {
		return err
	}
	if applied {
		return tx.Commit()
	}

	if _, err := tx.ExecContext(ctx, migration.SQL); err != nil {
		return fmt.Errorf("apply migration %s/%d %s: %w", namespace, migration.Version, migration.Name, err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO schema_migrations (namespace, version, name) VALUES ($1, $2, $3)
`, namespace, migration.Version, migration.Name); err != nil {
		return err
	}
	return tx.Commit()
}
