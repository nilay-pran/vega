package database

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Migrate applies every embedded migration whose version is newer than the
// last-applied one, each inside its own transaction. Migration files are named
// "NNNN_description.sql"; the leading number is the version. Forward-only.
func Migrate(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at INTEGER NOT NULL
	)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	var current int
	if err := db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return fmt.Errorf("read current version: %w", err)
	}

	files, err := fs.Glob(migrationsFS, "migrations/*.sql")
	if err != nil {
		return fmt.Errorf("list migrations: %w", err)
	}
	sort.Strings(files)

	for _, f := range files {
		version, err := parseVersion(f)
		if err != nil {
			return err
		}
		if version <= current {
			continue
		}
		body, err := migrationsFS.ReadFile(f)
		if err != nil {
			return fmt.Errorf("read %s: %w", f, err)
		}
		if err := applyOne(ctx, db, version, string(body)); err != nil {
			return fmt.Errorf("apply %s: %w", f, err)
		}
	}
	return nil
}

func applyOne(ctx context.Context, db *sql.DB, version int, body string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() // no-op after a successful Commit

	if _, err := tx.ExecContext(ctx, body); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
		version, time.Now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

// parseVersion extracts the leading integer from "migrations/0007_add_x.sql".
func parseVersion(file string) (int, error) {
	base := path.Base(file)
	num, _, _ := strings.Cut(base, "_")
	v, err := strconv.Atoi(num)
	if err != nil {
		return 0, fmt.Errorf("migration %q has no leading version number", file)
	}
	return v, nil
}
