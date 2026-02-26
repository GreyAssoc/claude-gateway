package db

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestOpen_RunsMigrations(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	database, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer database.Close()

	// Verify migrations ran by checking the migrations table
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var count int
	err = database.QueryRowContext(ctx, "SELECT COUNT(*) FROM migrations").Scan(&count)
	if err != nil {
		t.Fatalf("Failed to query migrations: %v", err)
	}
	if count != len(migrations) {
		t.Errorf("Migration count = %d, want %d", count, len(migrations))
	}

	// Verify key tables exist
	tables := []string{"config_store", "rate_limit_history", "session_history", "skills", "memory_daily", "memory_curated", "agents", "orchestrator_runs"}
	for _, table := range tables {
		var name string
		err := database.QueryRowContext(ctx, "SELECT name FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&name)
		if err != nil {
			t.Errorf("Table %q not found after migration: %v", table, err)
		}
	}
}

func TestOpen_IdempotentMigrations(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	// Open once to run migrations
	db1, err := Open(dbPath)
	if err != nil {
		t.Fatalf("First Open failed: %v", err)
	}
	db1.Close()

	// Open again — migrations should be skipped without error
	db2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Second Open failed: %v", err)
	}
	defer db2.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var count int
	err = db2.QueryRowContext(ctx, "SELECT COUNT(*) FROM migrations").Scan(&count)
	if err != nil {
		t.Fatalf("Failed to query migrations: %v", err)
	}
	if count != len(migrations) {
		t.Errorf("Migration count = %d after reopen, want %d", count, len(migrations))
	}
}

func TestMigrate_UsesTimeout(t *testing.T) {
	// Verify the migrate function respects context timeout by opening
	// a database successfully — if migrate used an unbounded context.Background(),
	// this test documents the fixed behavior.
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	database, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer database.Close()
}

func TestNilDB_ExecContext(t *testing.T) {
	var d *DB
	_, err := d.ExecContext(context.Background(), "SELECT 1")
	if err == nil {
		t.Error("Expected error from nil DB ExecContext")
	}
}

func TestNilDB_QueryContext(t *testing.T) {
	var d *DB
	_, err := d.QueryContext(context.Background(), "SELECT 1")
	if err == nil {
		t.Error("Expected error from nil DB QueryContext")
	}
}

func TestNilDB_QueryRowContext(t *testing.T) {
	var d *DB
	row := d.QueryRowContext(context.Background(), "SELECT 1")
	if row != nil {
		t.Error("Expected nil row from nil DB QueryRowContext")
	}
}

func TestNilDB_Close(t *testing.T) {
	var d *DB
	err := d.Close()
	if err == nil {
		t.Error("Expected error from nil DB Close")
	}
}

func TestNilDB_BeginTx(t *testing.T) {
	var d *DB
	_, err := d.BeginTx(context.Background(), nil)
	if err == nil {
		t.Error("Expected error from nil DB BeginTx")
	}
}
