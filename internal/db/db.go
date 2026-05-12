// Package db provides a SQLite database wrapper with WAL mode and migration support.
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"

	"github.com/you/claude-gateway/internal/format"
)

// DB wraps a SQLite database with WAL mode and migration support.
// The underlying sql.DB is unexported to limit the exposed API surface
// to only the methods this application actually uses.
type DB struct {
	db   *sql.DB
	path string
}

// Open creates or opens a SQLite database at the given path.
// Enables WAL mode and foreign keys.
func Open(dbPath string) (*DB, error) {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return nil, fmt.Errorf("creating db directory: %w", err)
	}

	sqlDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	// SQLite with WAL mode works best with a single connection.
	sqlDB.SetMaxOpenConns(1)

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
		db:   sqlDB,
		path: dbPath,
	}

	if err := db.migrate(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("running migrations: %w", err)
	}

	log.Printf("Database opened: %s", dbPath)
	return db, nil
}

// errNilDB is returned when a method is called on a nil *DB receiver.
var errNilDB = errors.New("db: operation on nil DB")

// Close closes the underlying database connection.
func (d *DB) Close() error {
	if d == nil {
		return errNilDB
	}
	return d.db.Close()
}

// ExecContext executes a query that doesn't return rows.
func (d *DB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if d == nil {
		return nil, errNilDB
	}
	return d.db.ExecContext(ctx, query, args...)
}

// QueryContext executes a query that returns rows.
func (d *DB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if d == nil {
		return nil, errNilDB
	}
	return d.db.QueryContext(ctx, query, args...)
}

// QueryRowContext executes a query expected to return at most one row.
func (d *DB) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	if d == nil {
		return nil
	}
	return d.db.QueryRowContext(ctx, query, args...)
}

// BeginTx starts a transaction.
func (d *DB) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	if d == nil {
		return nil, errNilDB
	}
	return d.db.BeginTx(ctx, opts)
}

// Tx executes a function within a transaction. Commits on success, rolls back on error.
// A deferred rollback ensures the transaction is cleaned up even if fn panics.
func (d *DB) Tx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	// Safety net: rollback on panic or if commit wasn't reached.
	// Rollback on an already-committed tx returns sql.ErrTxDone (harmless).
	defer func() { _ = tx.Rollback() }()

	if err := fn(tx); err != nil {
		return err
	}

	return tx.Commit()
}

// SetConfig stores a key-value pair in config_store.
func (d *DB) SetConfig(ctx context.Context, key, value string) error {
	_, err := d.ExecContext(ctx,
		`INSERT INTO config_store (key, value, updated_at) VALUES (?, ?, datetime('now'))
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value,
	)
	if err != nil {
		return fmt.Errorf("set config %q: %w", key, err)
	}
	return nil
}

// GetConfig retrieves a value from config_store. Returns empty string if not found.
func (d *DB) GetConfig(ctx context.Context, key string) (string, error) {
	var value string
	err := d.QueryRowContext(ctx, "SELECT value FROM config_store WHERE key = ?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get config %q: %w", key, err)
	}
	return value, nil
}

// RecordSession logs a conversation turn with token counts.
func (d *DB) RecordSession(ctx context.Context, sessionID, source, prompt string, inputTokens, outputTokens, cacheCreation, cacheRead int) error {
	_, err := d.ExecContext(ctx,
		`INSERT INTO session_history (session_id, source, prompt_preview, input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, datetime('now'))`,
		sessionID, source, format.Truncate(prompt, 200), inputTokens, outputTokens, cacheCreation, cacheRead,
	)
	if err != nil {
		return fmt.Errorf("record session: %w", err)
	}
	return nil
}

// CompactionRecord is a row in the session_compactions table.
//
// Each row is one auto- or manual-compaction event: the running token
// total crossed the threshold, the active model wrote a summary of the
// conversation, and a fresh SDK session was started seeded with that
// summary. The row records everything needed to replay or recall the
// superseded chunk.
type CompactionRecord struct {
	ID               int64
	SessionID        string
	OldSDKSessionID  string
	Summary          string
	Reason           string
	TriggeredBy      string
	UsageRatioPct    int
	ContextWindow    int
	TokensAtTrigger  int
	Provider         string
	Model            string
	CreatedAt        string
}

// RecordCompaction persists a session-compaction event. SessionID is the
// gateway-side logical session id (typically tg-<chat_id>). The summary
// is the model-written digest seeded into the next fresh SDK session.
func (d *DB) RecordCompaction(ctx context.Context, r CompactionRecord) error {
	_, err := d.ExecContext(ctx,
		`INSERT INTO session_compactions (
			session_id, old_sdk_session_id, summary, reason, triggered_by,
			usage_ratio_pct, context_window, tokens_at_trigger,
			provider, model, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, datetime('now'))`,
		r.SessionID, r.OldSDKSessionID, r.Summary, r.Reason, r.TriggeredBy,
		r.UsageRatioPct, r.ContextWindow, r.TokensAtTrigger,
		r.Provider, r.Model,
	)
	if err != nil {
		return fmt.Errorf("record compaction: %w", err)
	}
	return nil
}

// RecentCompactions returns the most recent compaction rows across all
// sessions. The memory module uses this to surface "what we summarised
// recently" as searchable historical context.
func (d *DB) RecentCompactions(ctx context.Context, limit int) ([]CompactionRecord, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := d.QueryContext(ctx,
		`SELECT id, session_id, COALESCE(old_sdk_session_id, ''), summary, reason,
		        triggered_by, usage_ratio_pct, context_window, tokens_at_trigger,
		        COALESCE(provider, ''), COALESCE(model, ''), created_at
		 FROM session_compactions
		 ORDER BY id DESC
		 LIMIT ?`, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("recent compactions: %w", err)
	}
	defer rows.Close()
	var out []CompactionRecord
	for rows.Next() {
		var r CompactionRecord
		if err := rows.Scan(&r.ID, &r.SessionID, &r.OldSDKSessionID, &r.Summary, &r.Reason,
			&r.TriggeredBy, &r.UsageRatioPct, &r.ContextWindow, &r.TokensAtTrigger,
			&r.Provider, &r.Model, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan compaction row: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CompactionsForSession returns the chronological compaction history of
// one logical session (oldest first). Useful when stitching a long
// conversation back together for replay or audit.
func (d *DB) CompactionsForSession(ctx context.Context, sessionID string) ([]CompactionRecord, error) {
	rows, err := d.QueryContext(ctx,
		`SELECT id, session_id, COALESCE(old_sdk_session_id, ''), summary, reason,
		        triggered_by, usage_ratio_pct, context_window, tokens_at_trigger,
		        COALESCE(provider, ''), COALESCE(model, ''), created_at
		 FROM session_compactions
		 WHERE session_id = ?
		 ORDER BY id ASC`, sessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("compactions for session: %w", err)
	}
	defer rows.Close()
	var out []CompactionRecord
	for rows.Next() {
		var r CompactionRecord
		if err := rows.Scan(&r.ID, &r.SessionID, &r.OldSDKSessionID, &r.Summary, &r.Reason,
			&r.TriggeredBy, &r.UsageRatioPct, &r.ContextWindow, &r.TokensAtTrigger,
			&r.Provider, &r.Model, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan compaction row: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RecordRateLimitSnapshot writes a rate limit window snapshot.
func (d *DB) RecordRateLimitSnapshot(ctx context.Context, requests, inputTokens, outputTokens int, headroom float64) error {
	_, err := d.ExecContext(ctx,
		`INSERT INTO rate_limit_history (requests, input_tokens, output_tokens, headroom, snapshot_at)
		 VALUES (?, ?, ?, ?, datetime('now'))`,
		requests, inputTokens, outputTokens, headroom,
	)
	if err != nil {
		return fmt.Errorf("record rate limit snapshot: %w", err)
	}
	return nil
}

