package database

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// open a fresh, migrated database in a temp dir.
func openMigrated(t *testing.T) *sql.DB {
	t.Helper()
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return db
}

func TestMigrateIsIdempotent(t *testing.T) {
	db := openMigrated(t)
	// Running migrations again must be a no-op, not an error.
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	var version int
	if err := db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatalf("read version: %v", err)
	}
	if version != 1 {
		t.Fatalf("schema version = %d, want 1", version)
	}
}

func TestInsertRoundTrip(t *testing.T) {
	db := openMigrated(t)
	now := time.Now().Unix()

	if _, err := db.Exec(
		`INSERT INTO users (id, email, created_at, updated_at) VALUES (?, ?, ?, ?)`,
		"u1", "a@example.com", now, now); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO uploads (id, user_id, source_path, filename, size_bytes, status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"up1", "u1", "/tmp/a.mov", "a.mov", 1024, "queued", now, now); err != nil {
		t.Fatalf("insert upload: %v", err)
	}

	var status string
	if err := db.QueryRow(`SELECT status FROM uploads WHERE id = ?`, "up1").Scan(&status); err != nil {
		t.Fatalf("read upload: %v", err)
	}
	if status != "queued" {
		t.Fatalf("status = %q, want queued", status)
	}
}

func TestForeignKeysEnforced(t *testing.T) {
	db := openMigrated(t)
	now := time.Now().Unix()
	// user_id "ghost" does not exist; the FK must reject this insert.
	_, err := db.Exec(
		`INSERT INTO uploads (id, user_id, source_path, filename, size_bytes, status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"up2", "ghost", "/tmp/b.mov", "b.mov", 1, "queued", now, now)
	if err == nil {
		t.Fatal("expected foreign key violation, got nil")
	}
}
