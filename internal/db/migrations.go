package db

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"
)

// migration represents a database schema migration.
type migration struct {
	version int
	name    string
	sql     string
}

// All migrations, in order. Never modify existing migrations — only append new ones.
var migrations = []migration{
	{
		version: 1,
		name:    "foundation",
		sql: `
			CREATE TABLE IF NOT EXISTS migrations (
				version  INTEGER PRIMARY KEY,
				name     TEXT NOT NULL,
				applied_at TEXT NOT NULL DEFAULT (datetime('now'))
			);

			CREATE TABLE IF NOT EXISTS config_store (
				key        TEXT PRIMARY KEY,
				value      TEXT NOT NULL,
				updated_at TEXT NOT NULL DEFAULT (datetime('now'))
			);

			CREATE TABLE IF NOT EXISTS rate_limit_history (
				id           INTEGER PRIMARY KEY AUTOINCREMENT,
				requests     INTEGER NOT NULL,
				input_tokens INTEGER NOT NULL,
				output_tokens INTEGER NOT NULL,
				headroom     REAL NOT NULL,
				snapshot_at  TEXT NOT NULL DEFAULT (datetime('now'))
			);
			CREATE INDEX IF NOT EXISTS idx_rlh_snapshot ON rate_limit_history(snapshot_at);

			CREATE TABLE IF NOT EXISTS session_history (
				id                    INTEGER PRIMARY KEY AUTOINCREMENT,
				session_id            TEXT NOT NULL,
				source                TEXT NOT NULL DEFAULT 'user',
				prompt_preview        TEXT,
				input_tokens          INTEGER NOT NULL DEFAULT 0,
				output_tokens         INTEGER NOT NULL DEFAULT 0,
				cache_creation_tokens INTEGER NOT NULL DEFAULT 0,
				cache_read_tokens     INTEGER NOT NULL DEFAULT 0,
				created_at            TEXT NOT NULL DEFAULT (datetime('now'))
			);
			CREATE INDEX IF NOT EXISTS idx_sh_session ON session_history(session_id);
			CREATE INDEX IF NOT EXISTS idx_sh_created ON session_history(created_at);
		`,
	},
	{
		version: 2,
		name:    "skills",
		sql: `
			CREATE TABLE IF NOT EXISTS skills (
				id           INTEGER PRIMARY KEY AUTOINCREMENT,
				name         TEXT NOT NULL UNIQUE,
				source_path  TEXT,
				content      TEXT NOT NULL,
				content_hash TEXT NOT NULL,
				precedence   INTEGER NOT NULL DEFAULT 0,
				enabled      INTEGER NOT NULL DEFAULT 1,
				created_at   TEXT NOT NULL DEFAULT (datetime('now')),
				updated_at   TEXT NOT NULL DEFAULT (datetime('now'))
			);
			CREATE INDEX IF NOT EXISTS idx_skills_name ON skills(name);
			CREATE INDEX IF NOT EXISTS idx_skills_enabled ON skills(enabled);
		`,
	},
	{
		version: 3,
		name:    "memory",
		sql: `
			CREATE TABLE IF NOT EXISTS memory_daily (
				id         INTEGER PRIMARY KEY AUTOINCREMENT,
				date       TEXT NOT NULL UNIQUE,
				summary    TEXT NOT NULL,
				key_events TEXT DEFAULT '[]',
				created_at TEXT NOT NULL DEFAULT (datetime('now'))
			);
			CREATE INDEX IF NOT EXISTS idx_md_date ON memory_daily(date);

			CREATE TABLE IF NOT EXISTS memory_curated (
				id         INTEGER PRIMARY KEY AUTOINCREMENT,
				category   TEXT NOT NULL,
				content    TEXT NOT NULL,
				source     TEXT DEFAULT 'user',
				importance INTEGER NOT NULL DEFAULT 5 CHECK(importance BETWEEN 1 AND 10),
				created_at TEXT NOT NULL DEFAULT (datetime('now')),
				updated_at TEXT NOT NULL DEFAULT (datetime('now'))
			);
			CREATE INDEX IF NOT EXISTS idx_mc_importance ON memory_curated(importance DESC);
			CREATE INDEX IF NOT EXISTS idx_mc_category ON memory_curated(category);

			CREATE TABLE IF NOT EXISTS bootstrap_files (
				id           INTEGER PRIMARY KEY AUTOINCREMENT,
				name         TEXT NOT NULL UNIQUE,
				content      TEXT NOT NULL,
				content_hash TEXT NOT NULL,
				updated_at   TEXT NOT NULL DEFAULT (datetime('now'))
			);
		`,
	},
	{
		version: 4,
		name:    "rate_limit_events",
		sql: `
			CREATE TABLE IF NOT EXISTS rate_limit_events (
				id               INTEGER PRIMARY KEY AUTOINCREMENT,
				event_type       TEXT NOT NULL,
				session_id       TEXT,
				source           TEXT NOT NULL DEFAULT 'user',
				estimated_tokens INTEGER DEFAULT 0,
				actual_tokens    INTEGER DEFAULT 0,
				wait_ms          INTEGER DEFAULT 0,
				error_message    TEXT,
				created_at       TEXT NOT NULL DEFAULT (datetime('now'))
			);
			CREATE INDEX IF NOT EXISTS idx_rle_type ON rate_limit_events(event_type);
			CREATE INDEX IF NOT EXISTS idx_rle_created ON rate_limit_events(created_at);
			CREATE INDEX IF NOT EXISTS idx_rle_source ON rate_limit_events(source);
		`,
	},
	{
		version: 5,
		name:    "orchestrator",
		sql: `
			CREATE TABLE IF NOT EXISTS agents (
				id        INTEGER PRIMARY KEY AUTOINCREMENT,
				name      TEXT NOT NULL UNIQUE,
				dna_file  TEXT NOT NULL,
				tier      INTEGER NOT NULL DEFAULT 4,
				weight    INTEGER NOT NULL DEFAULT 1,
				has_veto  INTEGER NOT NULL DEFAULT 0,
				team      TEXT NOT NULL,
				enabled   INTEGER NOT NULL DEFAULT 1,
				dna_hash  TEXT NOT NULL,
				updated_at TEXT NOT NULL DEFAULT (datetime('now'))
			);
			CREATE INDEX IF NOT EXISTS idx_agents_team ON agents(team);

			CREATE TABLE IF NOT EXISTS orchestrator_runs (
				id             INTEGER PRIMARY KEY AUTOINCREMENT,
				team           TEXT NOT NULL,
				task           TEXT NOT NULL,
				project_path   TEXT,
				outcome        TEXT NOT NULL DEFAULT 'pending',
				consensus_json TEXT DEFAULT '{}',
				verdicts_json  TEXT DEFAULT '[]',
				duration_ms    INTEGER DEFAULT 0,
				created_at     TEXT NOT NULL DEFAULT (datetime('now'))
			);
			CREATE INDEX IF NOT EXISTS idx_or_team ON orchestrator_runs(team);
			CREATE INDEX IF NOT EXISTS idx_or_created ON orchestrator_runs(created_at);

			CREATE TABLE IF NOT EXISTS orchestrator_verdicts (
				id           INTEGER PRIMARY KEY AUTOINCREMENT,
				run_id       INTEGER NOT NULL REFERENCES orchestrator_runs(id),
				agent_name   TEXT NOT NULL,
				verdict      TEXT NOT NULL,
				confidence   REAL DEFAULT 0,
				issues_json  TEXT DEFAULT '[]',
				duration_ms  INTEGER DEFAULT 0,
				created_at   TEXT NOT NULL DEFAULT (datetime('now'))
			);
			CREATE INDEX IF NOT EXISTS idx_ov_run ON orchestrator_verdicts(run_id);
		`,
	},
	{
		version: 6,
		name:    "session_compactions",
		sql: `
			CREATE TABLE IF NOT EXISTS session_compactions (
				id                   INTEGER PRIMARY KEY AUTOINCREMENT,
				session_id           TEXT NOT NULL,
				old_sdk_session_id   TEXT,
				summary              TEXT NOT NULL,
				reason               TEXT NOT NULL,           -- 'threshold_80pct', 'manual', etc.
				triggered_by         TEXT NOT NULL DEFAULT 'auto', -- 'auto' | 'manual'
				usage_ratio_pct      INTEGER NOT NULL DEFAULT 0,   -- 0..100
				context_window       INTEGER NOT NULL DEFAULT 0,
				tokens_at_trigger    INTEGER NOT NULL DEFAULT 0,
				provider             TEXT,
				model                TEXT,
				created_at           TEXT NOT NULL DEFAULT (datetime('now'))
			);
			CREATE INDEX IF NOT EXISTS idx_sc_session   ON session_compactions(session_id);
			CREATE INDEX IF NOT EXISTS idx_sc_created   ON session_compactions(created_at);
		`,
	},
}

// migrate runs all pending migrations in order.
func (d *DB) migrate() error {
	// Ensure migrations table exists (bootstrap)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := d.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS migrations (
		version    INTEGER PRIMARY KEY,
		name       TEXT NOT NULL,
		applied_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`)
	if err != nil {
		return fmt.Errorf("creating migrations table: %w", err)
	}

	// Get current version
	var current int
	err = d.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM migrations").Scan(&current)
	if err != nil {
		return fmt.Errorf("checking migration version: %w", err)
	}

	for _, m := range migrations {
		if m.version <= current {
			continue
		}

		log.Printf("Running migration %d: %s", m.version, m.name)

		if err := d.Tx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.Exec(m.sql); err != nil {
				return fmt.Errorf("migration %d (%s): %w", m.version, m.name, err)
			}
			if _, err := tx.Exec(
				"INSERT INTO migrations (version, name) VALUES (?, ?)",
				m.version, m.name,
			); err != nil {
				return fmt.Errorf("recording migration %d: %w", m.version, err)
			}
			return nil
		}); err != nil {
			return err
		}

		log.Printf("Migration %d complete", m.version)
	}

	return nil
}
