package db

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"

	_ "modernc.org/sqlite"
)

// DB wraps a SQLite database with WAL mode and migration support.
type DB struct {
	*sql.DB
	mu   sync.RWMutex
	path string
}

// Open creates or opens a SQLite database at the given path.
// Enables WAL mode and foreign keys.
func Open(dbPath string) (*DB, error) {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return nil, fmt.Errorf("creating db directory: %w", err)
	}

	sqlDB, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=ON")
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	// Verify connection
	if err := sqlDB.Ping(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("pinging database: %w", err)
	}

	// Set pragmas for performance
	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA cache_size=-64000", // 64MB
		"PRAGMA busy_timeout=5000",
		"PRAGMA foreign_keys=ON",
	}
	for _, p := range pragmas {
		if _, err := sqlDB.Exec(p); err != nil {
			sqlDB.Close()
			return nil, fmt.Errorf("setting pragma %q: %w", p, err)
		}
	}

	db := &DB{
		DB:   sqlDB,
		path: dbPath,
	}

	if err := db.migrate(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("running migrations: %w", err)
	}

	log.Printf("Database opened: %s", dbPath)
	return db, nil
}

// Tx executes a function within a transaction. Commits on success, rolls back on error.
func (db *DB) Tx(fn func(tx *sql.Tx) error) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}

	if err := fn(tx); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil {
			return fmt.Errorf("rollback failed: %v (original: %w)", rbErr, err)
		}
		return err
	}

	return tx.Commit()
}

// SetConfig stores a key-value pair in config_store.
func (db *DB) SetConfig(key, value string) error {
	_, err := db.Exec(
		`INSERT INTO config_store (key, value, updated_at) VALUES (?, ?, datetime('now'))
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value,
	)
	return err
}

// GetConfig retrieves a value from config_store. Returns empty string if not found.
func (db *DB) GetConfig(key string) (string, error) {
	var value string
	err := db.QueryRow("SELECT value FROM config_store WHERE key = ?", key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, err
}

// RecordSession logs a conversation turn with token counts.
func (db *DB) RecordSession(sessionID, source, prompt string, inputTokens, outputTokens, cacheCreation, cacheRead int) error {
	_, err := db.Exec(
		`INSERT INTO session_history (session_id, source, prompt_preview, input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, datetime('now'))`,
		sessionID, source, truncate(prompt, 200), inputTokens, outputTokens, cacheCreation, cacheRead,
	)
	return err
}

// RecordRateLimitSnapshot writes a rate limit window snapshot.
func (db *DB) RecordRateLimitSnapshot(requests, inputTokens, outputTokens int, headroom float64) error {
	_, err := db.Exec(
		`INSERT INTO rate_limit_history (requests, input_tokens, output_tokens, headroom, snapshot_at)
		 VALUES (?, ?, ?, ?, datetime('now'))`,
		requests, inputTokens, outputTokens, headroom,
	)
	return err
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}
