package pgdb

import (
	"context"
	"fmt"
	"log"
)

// EnsureSchema creates the agents table and conversations.agent_id on an
// existing database. Idempotent — safe on every Open().
func (d *DB) EnsureSchema(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS agents (
			id              SERIAL PRIMARY KEY,
			handle          TEXT NOT NULL UNIQUE,
			handle_key      TEXT NOT NULL UNIQUE,
			display_name    TEXT NOT NULL,
			dir             TEXT,
			project_path    TEXT,
			framework       TEXT,
			source          TEXT NOT NULL DEFAULT 'folder',
			is_active       BOOLEAN NOT NULL DEFAULT TRUE,
			created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_agents_handle_key ON agents (handle_key)`,
		`CREATE INDEX IF NOT EXISTS idx_agents_active ON agents (is_active) WHERE is_active = TRUE`,
	}
	for _, stmt := range stmts {
		if _, err := d.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("ensure schema: %w", err)
		}
	}
	// No FK: conversations is often owned by the postgres superuser, and
	// REFERENCES would make this ALTER fail and (previously) kill startup.
	// A non-owner still gets 42501 from ADD COLUMN IF NOT EXISTS even when
	// the column is already there, so only log when it is actually missing.
	if _, err := d.db.ExecContext(ctx, `ALTER TABLE conversations ADD COLUMN IF NOT EXISTS agent_id INT`); err != nil {
		if !d.hasAgentColumn(ctx) {
			log.Printf("pgdb: add conversations.agent_id: %v", err)
		}
	} else if _, err := d.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_conv_agent ON conversations (agent_id)`); err != nil {
		log.Printf("pgdb: idx_conv_agent: %v", err)
	}
	return nil
}

func (d *DB) hasAgentColumn(ctx context.Context) bool {
	var n int
	err := d.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'conversations' AND column_name = 'agent_id'`).Scan(&n)
	return err == nil && n > 0
}

// EnsureBuiltinAgents registers the system handles that are not folders.
func (d *DB) EnsureBuiltinAgents(ctx context.Context) error {
	builtins := []AgentSpec{
		{Handle: AgentHandleDefault, DisplayName: "default", Source: "builtin"},
		{Handle: AgentHandleCron, DisplayName: "cron", Source: "system"},
		{Handle: AgentHandleOrchestrator, DisplayName: "orchestrator", Source: "system"},
		{Handle: AgentHandleWatcher, DisplayName: "watcher", Source: "system"},
	}
	for _, spec := range builtins {
		if _, err := d.UpsertAgent(ctx, spec); err != nil {
			return err
		}
	}
	return nil
}

// BackfillConversationAgents sets agent_id on rows that predate the column.
// Unknown session suffixes become stub agents (source=auto) so nothing is left unlinked.
func (d *DB) BackfillConversationAgents(ctx context.Context) (int64, error) {
	res, err := d.db.ExecContext(ctx, `
		UPDATE conversations c
		SET agent_id = a.id
		FROM agents a
		WHERE c.agent_id IS NULL
		  AND a.handle_key = CASE
		        WHEN position('-ag-' IN c.session_id) > 0
		            THEN regexp_replace(c.session_id, '^.*-ag-', '')
		        WHEN c.session_id LIKE 'cron-%' OR c.session_id LIKE '%:cron-%' THEN 'cron'
		        WHEN c.session_id LIKE 'watcher-%' OR c.session_id LIKE '%:watcher-%' THEN 'watcher'
		        WHEN c.session_id LIKE 'orch-%' OR c.session_id LIKE '%:orch-%' THEN 'orchestrator'
		        ELSE 'default'
		      END`)
	if err != nil {
		return 0, fmt.Errorf("backfill conversations: %w", err)
	}
	n, _ := res.RowsAffected()

	rows, err := d.db.QueryContext(ctx, `SELECT DISTINCT session_id FROM conversations WHERE agent_id IS NULL`)
	if err != nil {
		return n, fmt.Errorf("backfill remaining sessions: %w", err)
	}
	defer rows.Close()

	var leftover []string
	for rows.Next() {
		var sid string
		if err := rows.Scan(&sid); err != nil {
			return n, err
		}
		leftover = append(leftover, sid)
	}
	if err := rows.Err(); err != nil {
		return n, err
	}

	var extra int64
	for _, sid := range leftover {
		id, err := d.EnsureAgentIDForSession(ctx, sid)
		if err != nil {
			log.Printf("pgdb: backfill stub for %s: %v", sid, err)
			continue
		}
		u, err := d.db.ExecContext(ctx,
			`UPDATE conversations SET agent_id = $1 WHERE session_id = $2 AND agent_id IS NULL`,
			id, sid)
		if err != nil {
			log.Printf("pgdb: backfill update %s: %v", sid, err)
			continue
		}
		k, _ := u.RowsAffected()
		extra += k
	}
	return n + extra, nil
}
