package pgdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/lib/pq"
)

const (
	AgentHandleDefault      = "default"
	AgentHandleCron         = "cron"
	AgentHandleOrchestrator = "orchestrator"
	AgentHandleWatcher      = "watcher"
)

// Agent is a row in the PostgreSQL agents table (Telegram specialists + system handles).
type Agent struct {
	ID          int
	Handle      string
	HandleKey   string
	DisplayName string
	Dir         string
	ProjectPath string
	Framework   string
	Source      string
	IsActive    bool
}

// AgentSpec is the upsert payload when registering an agent from disk or a session.
type AgentSpec struct {
	Handle      string
	DisplayName string
	Dir         string
	ProjectPath string
	Framework   string
	Source      string
}

// NormalizeHandle matches Telegram slot keys: lowercase letters and digits only.
func NormalizeHandle(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// AgentKeyFromSession maps a conversation session_id to agents.handle_key.
//
//	tg-42                    → default
//	tg-42-ag-greywebsite     → greywebsite
//	qwen:tg-42-ag-greywebsite → greywebsite
//	cron-standup             → cron
//	cron-seo-ag-greywebsite  → greywebsite
//	watcher-gmail            → watcher
func AgentKeyFromSession(sessionID string) string {
	s := strings.TrimSpace(sessionID)
	if s == "" {
		return AgentHandleDefault
	}
	if i := strings.IndexByte(s, ':'); i > 0 && i < 16 {
		rest := s[i+1:]
		if rest != "" && (unicode.IsLetter(rune(s[0]))) {
			s = rest
		}
	}
	if i := strings.LastIndex(s, "-ag-"); i >= 0 {
		key := NormalizeHandle(s[i+len("-ag-"):])
		if key != "" {
			return key
		}
	}
	switch {
	case s == AgentHandleCron || strings.HasPrefix(s, "cron-"):
		return AgentHandleCron
	case s == AgentHandleWatcher || strings.HasPrefix(s, "watcher-"):
		return AgentHandleWatcher
	case s == AgentHandleOrchestrator || strings.HasPrefix(s, "orch-"):
		return AgentHandleOrchestrator
	}
	return AgentHandleDefault
}

// UpsertAgent inserts or updates an agent by handle_key and returns its id.
func (d *DB) UpsertAgent(ctx context.Context, spec AgentSpec) (int, error) {
	handle := strings.TrimSpace(spec.Handle)
	if handle == "" {
		return 0, fmt.Errorf("upsert agent: empty handle")
	}
	key := NormalizeHandle(handle)
	if key == "" {
		return 0, fmt.Errorf("upsert agent: handle %q has no letters/digits", handle)
	}
	display := strings.TrimSpace(spec.DisplayName)
	if display == "" {
		display = handle
	}
	source := strings.TrimSpace(spec.Source)
	if source == "" {
		source = "folder"
	}

	var id int
	err := d.db.QueryRowContext(ctx, `
		INSERT INTO agents (handle, handle_key, display_name, dir, project_path, framework, source, is_active, updated_at)
		VALUES ($1, $2, $3, NULLIF($4, ''), NULLIF($5, ''), NULLIF($6, ''), $7, TRUE, NOW())
		ON CONFLICT (handle_key) DO UPDATE SET
			handle = CASE
				WHEN agents.source IN ('folder', 'builtin', 'system') AND EXCLUDED.source = 'auto'
					THEN agents.handle
				ELSE EXCLUDED.handle
			END,
			display_name = CASE
				WHEN agents.source IN ('folder', 'builtin', 'system') AND EXCLUDED.source = 'auto'
					THEN agents.display_name
				ELSE EXCLUDED.display_name
			END,
			dir = COALESCE(EXCLUDED.dir, agents.dir),
			project_path = COALESCE(EXCLUDED.project_path, agents.project_path),
			framework = COALESCE(EXCLUDED.framework, agents.framework),
			source = CASE
				WHEN agents.source IN ('folder', 'builtin', 'system') AND EXCLUDED.source = 'auto'
					THEN agents.source
				ELSE EXCLUDED.source
			END,
			is_active = TRUE,
			updated_at = NOW()
		RETURNING id`,
		handle, key, display, spec.Dir, spec.ProjectPath, spec.Framework, source,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("upsert agent %s: %w", handle, err)
	}
	return id, nil
}

// EnsureAgentIDForSession returns the agent id for a session, creating a stub if needed.
func (d *DB) EnsureAgentIDForSession(ctx context.Context, sessionID string) (int, error) {
	key := AgentKeyFromSession(sessionID)
	var id int
	err := d.db.QueryRowContext(ctx, `SELECT id FROM agents WHERE handle_key = $1`, key).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("lookup agent %s: %w", key, err)
	}
	return d.UpsertAgent(ctx, AgentSpec{
		Handle:      key,
		DisplayName: key,
		Source:      "auto",
	})
}

// SyncChatAgents upserts folder specialists and deactivates folders that disappeared.
func (d *DB) SyncChatAgents(ctx context.Context, specs []AgentSpec) error {
	keys := make([]string, 0, len(specs))
	for _, spec := range specs {
		if spec.Source == "" {
			spec.Source = "folder"
		}
		if _, err := d.UpsertAgent(ctx, spec); err != nil {
			return err
		}
		keys = append(keys, NormalizeHandle(spec.Handle))
	}
	if len(keys) == 0 {
		_, err := d.db.ExecContext(ctx, `
			UPDATE agents SET is_active = FALSE, updated_at = NOW()
			WHERE source = 'folder' AND is_active`)
		return err
	}
	_, err := d.db.ExecContext(ctx, `
		UPDATE agents SET is_active = FALSE, updated_at = NOW()
		WHERE source = 'folder' AND is_active AND NOT (handle_key = ANY($1))`,
		pq.Array(keys))
	if err != nil {
		return fmt.Errorf("deactivate missing folder agents: %w", err)
	}
	return nil
}

// ListAgents returns registered agents, newest-updated last.
func (d *DB) ListAgents(ctx context.Context, activeOnly bool) ([]Agent, error) {
	q := `
		SELECT id, handle, handle_key, display_name,
		       COALESCE(dir, ''), COALESCE(project_path, ''), COALESCE(framework, ''),
		       source, is_active
		FROM agents`
	if activeOnly {
		q += ` WHERE is_active = TRUE`
	}
	q += ` ORDER BY source, handle`
	rows, err := d.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	defer rows.Close()

	var out []Agent
	for rows.Next() {
		var a Agent
		if err := rows.Scan(&a.ID, &a.Handle, &a.HandleKey, &a.DisplayName,
			&a.Dir, &a.ProjectPath, &a.Framework, &a.Source, &a.IsActive); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
