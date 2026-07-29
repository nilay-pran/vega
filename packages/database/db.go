// Package database owns the desktop engine's SQLite store: opening it with the
// right pragmas and applying versioned migrations. The upload server uses a
// different store (Postgres); this package is client-side only.
package database

import (
	"context"
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite" // pure-Go driver, no cgo → trivial cross-compilation
)

// Open opens (creating if needed) the SQLite database at path.
//
// Pragmas are passed in the DSN so they apply to every pooled connection:
//   - WAL          : concurrent reads while a write is in flight, durable commits
//   - busy_timeout : wait instead of failing when briefly locked
//   - foreign_keys : enforce the FK constraints declared in the schema
//   - synchronous=NORMAL : safe under WAL, far fewer fsyncs than FULL
//
// The daemon is the single writer, so we cap the pool at one connection. That
// removes "database is locked" races entirely and keeps the model simple.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	dsn := fmt.Sprintf(
		"file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(1)",
		path,
	)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %q: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite %q: %w", path, err)
	}
	return db, nil
}
