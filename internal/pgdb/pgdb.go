// Package pgdb provides the PostgreSQL client for persistent conversation memory,
// semantic search, people contacts, daily journals, and user profiling.
package pgdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"slices"
	"strconv"
	"strings"
	"time"

	_ "github.com/lib/pq"
)

// ErrNotFound is returned when an update or delete affects zero rows.
var ErrNotFound = errors.New("not found")

// DB wraps a PostgreSQL connection pool.
type DB struct {
	db       *sql.DB
	agentCol bool // conversations.agent_id exists (schema migrate may have been skipped)
}

// Open connects to PostgreSQL and verifies connectivity.
func Open(connStr string) (*DB, error) {
	if connStr == "" {
		return nil, fmt.Errorf("postgres connection string is empty")
	}
	sqlDB, err := sql.Open("postgres", connStr)
	if err != nil {
		return nil, fmt.Errorf("opening postgres: %w", err)
	}

	sqlDB.SetMaxOpenConns(10)
	sqlDB.SetMaxIdleConns(5)
	sqlDB.SetConnMaxLifetime(30 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if err := sqlDB.PingContext(ctx); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("pinging postgres: %w", err)
	}

	d := &DB{db: sqlDB}
	// Schema migrate must never prevent Telegram from polling. The gateway
	// role may lack ALTER on conversations (owned by postgres); log and continue.
	if err := d.EnsureSchema(ctx); err != nil {
		log.Printf("pgdb: schema migrate failed (gateway will still start): %v", err)
	}
	d.agentCol = d.hasAgentColumn(ctx)
	if d.agentCol {
		if err := d.EnsureBuiltinAgents(ctx); err != nil {
			log.Printf("pgdb: builtin agents: %v", err)
		} else if n, err := d.BackfillConversationAgents(ctx); err != nil {
			log.Printf("pgdb: backfill conversation agents: %v", err)
		} else if n > 0 {
			log.Printf("pgdb: backfilled agent_id on %d conversation rows", n)
		}
	}
	log.Printf("pgdb: connected (conversations.agent_id=%v)", d.agentCol)
	return d, nil
}

// HasAgentColumn reports whether conversations.agent_id is present.
func (d *DB) HasAgentColumn() bool {
	return d.agentCol
}

// Close closes the connection pool.
func (d *DB) Close() error {
	return d.db.Close()
}

// vectorToString formats a float32 slice as a pgvector literal: [0.1,0.2,...]
func vectorToString(v []float32) string {
	if len(v) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteByte('[')
	for i, f := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(f), 'f', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}

// ================================================================
// Types
// ================================================================

// Conversation represents a single user or assistant message.
type Conversation struct {
	ID            int64
	SessionID     string
	Role          string
	Content       string
	TokenEstimate int
	CreatedAt     time.Time
	AgentID       int64
	AgentHandle   string
}

const conversationSelect = `c.id, c.session_id, c.role, c.content, COALESCE(c.token_estimate, 0), c.created_at, COALESCE(c.agent_id, 0), COALESCE(a.handle, '')`
const conversationFrom = `conversations c LEFT JOIN agents a ON a.id = c.agent_id`

// Used when startup could not add conversations.agent_id (gateway is not
// the table owner). Same scan shape as conversationSelect: agent id 0 and
// an empty handle. Referencing the missing column makes every history read
// fail and the next prompt goes out with no prior turns.
const conversationSelectNoAgent = `c.id, c.session_id, c.role, c.content, COALESCE(c.token_estimate, 0), c.created_at, 0::bigint, ''::text`
const conversationFromNoAgent = `conversations c`

// conversationSQL returns the SELECT list and FROM clause that match the
// live schema. Callers must keep using scanConversation's column order.
func (d *DB) conversationSQL() (selectList, from string) {
	if d.agentCol {
		return conversationSelect, conversationFrom
	}
	return conversationSelectNoAgent, conversationFromNoAgent
}

func scanConversation(s interface{ Scan(...any) error }) (Conversation, error) {
	var c Conversation
	err := s.Scan(&c.ID, &c.SessionID, &c.Role, &c.Content, &c.TokenEstimate, &c.CreatedAt, &c.AgentID, &c.AgentHandle)
	return c, err
}

// ConversationHit is a Conversation with a similarity score from semantic search.
type ConversationHit struct {
	Conversation
	Similarity float64
}

// MemoryEntry represents a bootstrap instruction, preference, or learned fact.
type MemoryEntry struct {
	ID         int
	Category   string
	Key        string
	Content    string
	Importance int
	Source     string
	IsActive   bool
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Person represents a contact in the people database.
type Person struct {
	ID            int
	FullName      string
	PreferredName string
	Type          string
	Role          string
	Company       string
	Notes         string
	IsActive      bool
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// ContactDetail represents a single contact method for a person.
type ContactDetail struct {
	ID          int
	PersonID    int
	ContactType string
	Value       string
	Label       string
	IsPrimary   bool
}

// PersonWithContacts combines a Person with their contact details.
type PersonWithContacts struct {
	Person
	Contacts []ContactDetail
}

// JournalEntry represents a daily summary.
type JournalEntry struct {
	ID        int
	Date      string
	Summary   string
	Decisions json.RawMessage
	Actions   json.RawMessage
	Topics    json.RawMessage
	Projects  json.RawMessage
	Stats     json.RawMessage
	CreatedAt time.Time
}

// ProfileAspect represents a single aspect of the user profile.
type ProfileAspect struct {
	ID         int
	Aspect     string
	Content    string
	Evidence   json.RawMessage
	Confidence float64
	UpdatedAt  time.Time
}

// ================================================================
// Conversations
// ================================================================

// SaveConversation persists a message with optional embedding.
func (d *DB) SaveConversation(ctx context.Context, sessionID, role, content string, embedding []float32, tokenEstimate int) error {
	var embStr *string
	if len(embedding) > 0 {
		s := vectorToString(embedding)
		embStr = &s
	}
	if d.agentCol {
		agentID, err := d.EnsureAgentIDForSession(ctx, sessionID)
		if err != nil {
			return fmt.Errorf("save conversation: %w", err)
		}
		_, err = d.db.ExecContext(ctx,
			`INSERT INTO conversations (session_id, role, content, embedding, token_estimate, agent_id)
			 VALUES ($1, $2, $3, $4::vector, $5, $6)`,
			sessionID, role, content, embStr, tokenEstimate, agentID,
		)
		if err != nil {
			return fmt.Errorf("save conversation: %w", err)
		}
		return nil
	}
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO conversations (session_id, role, content, embedding, token_estimate)
		 VALUES ($1, $2, $3, $4::vector, $5)`,
		sessionID, role, content, embStr, tokenEstimate,
	)
	if err != nil {
		return fmt.Errorf("save conversation: %w", err)
	}
	return nil
}

// SaveConversationReturningID persists a message without an embedding and
// returns the row ID. Used for the "save now, embed later" pattern so that
// the next message's enriched prompt always sees the latest conversation
// turns even when embedding generation is still in-flight.
func (d *DB) SaveConversationReturningID(ctx context.Context, sessionID, role, content string, tokenEstimate int) (int64, error) {
	var id int64
	if d.agentCol {
		agentID, err := d.EnsureAgentIDForSession(ctx, sessionID)
		if err != nil {
			return 0, fmt.Errorf("save conversation returning id: %w", err)
		}
		err = d.db.QueryRowContext(ctx,
			`INSERT INTO conversations (session_id, role, content, token_estimate, agent_id)
			 VALUES ($1, $2, $3, $4, $5)
			 RETURNING id`,
			sessionID, role, content, tokenEstimate, agentID,
		).Scan(&id)
		if err != nil {
			return 0, fmt.Errorf("save conversation returning id: %w", err)
		}
		return id, nil
	}
	err := d.db.QueryRowContext(ctx,
		`INSERT INTO conversations (session_id, role, content, token_estimate)
		 VALUES ($1, $2, $3, $4)
		 RETURNING id`,
		sessionID, role, content, tokenEstimate,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("save conversation returning id: %w", err)
	}
	return id, nil
}

// UpdateConversationEmbedding sets the embedding for an existing conversation
// row. Called asynchronously after SaveConversationReturningID.
func (d *DB) UpdateConversationEmbedding(ctx context.Context, id int64, embedding []float32) error {
	if len(embedding) == 0 {
		return nil
	}
	s := vectorToString(embedding)
	_, err := d.db.ExecContext(ctx,
		`UPDATE conversations SET embedding = $1::vector WHERE id = $2`,
		s, id,
	)
	if err != nil {
		return fmt.Errorf("update conversation embedding: %w", err)
	}
	return nil
}

// RecentConversations returns the last N messages for a specific session.
func (d *DB) RecentConversations(ctx context.Context, sessionID string, limit int) ([]Conversation, error) {
	if limit <= 0 {
		return nil, nil
	}
	cols, from := d.conversationSQL()
	rows, err := d.db.QueryContext(ctx,
		`SELECT `+cols+`
		 FROM `+from+`
		 WHERE c.session_id = $1
		 ORDER BY c.created_at DESC
		 LIMIT $2`,
		sessionID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("recent conversations: %w", err)
	}
	defer rows.Close()

	var convs []Conversation
	for rows.Next() {
		c, err := scanConversation(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning conversation: %w", err)
		}
		convs = append(convs, c)
	}
	// Reverse so oldest is first
	slices.Reverse(convs)
	return convs, rows.Err()
}

// RecentConversationsAllSessions returns the last N messages across all sessions.
// Ordered oldest-first for context injection.
func (d *DB) RecentConversationsAllSessions(ctx context.Context, limit int) ([]Conversation, error) {
	if limit <= 0 {
		return nil, nil
	}
	cols, from := d.conversationSQL()
	rows, err := d.db.QueryContext(ctx,
		`SELECT `+cols+`
		 FROM `+from+`
		 ORDER BY c.created_at DESC
		 LIMIT $1`,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("recent conversations all sessions: %w", err)
	}
	defer rows.Close()

	var convs []Conversation
	for rows.Next() {
		c, err := scanConversation(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning conversation: %w", err)
		}
		convs = append(convs, c)
	}
	// Reverse so oldest is first
	slices.Reverse(convs)
	return convs, rows.Err()
}

// ConversationsByDate returns conversations for a specific date (YYYY-MM-DD).
func (d *DB) ConversationsByDate(ctx context.Context, date string) ([]Conversation, error) {
	if _, err := time.Parse("2006-01-02", date); err != nil {
		return nil, fmt.Errorf("conversations by date: invalid date %q: %w", date, err)
	}
	cols, from := d.conversationSQL()
	rows, err := d.db.QueryContext(ctx,
		`SELECT `+cols+`
		 FROM `+from+`
		 WHERE c.created_at::date = $1::date
		 ORDER BY c.created_at ASC`,
		date,
	)
	if err != nil {
		return nil, fmt.Errorf("conversations by date: %w", err)
	}
	defer rows.Close()

	var convs []Conversation
	for rows.Next() {
		c, err := scanConversation(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning conversation: %w", err)
		}
		convs = append(convs, c)
	}
	return convs, rows.Err()
}

// SearchSimilar finds conversations semantically similar to the given embedding.
// Excludes conversations with IDs in excludeIDs (to avoid duplicating the recency window).
// If sessionID is non-empty, only that agent's thread is searched so named
// chats cannot pull in another agent's history.
func (d *DB) SearchSimilar(ctx context.Context, embedding []float32, excludeIDs []int64, limit int, minSimilarity float64, sessionID string) ([]ConversationHit, error) {
	if len(embedding) == 0 {
		return nil, nil
	}
	if limit <= 0 {
		return nil, nil
	}
	embStr := vectorToString(embedding)

	// Build exclude list.
	// Safety: excludeIDs is []int64, and strconv.FormatInt produces only
	// digit characters — no SQL injection is possible from these values.
	excludeClause := ""
	if len(excludeIDs) > 0 {
		parts := make([]string, len(excludeIDs))
		for i, id := range excludeIDs {
			parts[i] = strconv.FormatInt(id, 10)
		}
		excludeClause = " AND c.id NOT IN (" + strings.Join(parts, ",") + ")"
	}

	sessionClause := ""
	args := []any{embStr, limit, minSimilarity}
	if sessionID != "" {
		sessionClause = " AND c.session_id = $4"
		args = append(args, sessionID)
	}

	cols, from := d.conversationSQL()
	query := fmt.Sprintf(
		`SELECT `+cols+`,
		        1 - (c.embedding <=> $1::vector) AS similarity
		 FROM `+from+`
		 WHERE c.embedding IS NOT NULL
		   AND 1 - (c.embedding <=> $1::vector) >= $3%s%s
		 ORDER BY c.embedding <=> $1::vector
		 LIMIT $2`,
		excludeClause, sessionClause,
	)

	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("search similar: %w", err)
	}
	defer rows.Close()

	var hits []ConversationHit
	for rows.Next() {
		var h ConversationHit
		if err := rows.Scan(&h.ID, &h.SessionID, &h.Role, &h.Content, &h.TokenEstimate, &h.CreatedAt, &h.AgentID, &h.AgentHandle, &h.Similarity); err != nil {
			return nil, fmt.Errorf("scanning hit: %w", err)
		}
		hits = append(hits, h)
	}
	return hits, rows.Err()
}

// ================================================================
// Memory Entries
// ================================================================

// SaveMemoryEntry creates a new memory entry. Returns the new entry's ID.
func (d *DB) SaveMemoryEntry(ctx context.Context, category, key, content, source string, importance int) (int, error) {
	if importance < 1 {
		importance = 1
	}
	if importance > 10 {
		importance = 10
	}

	var id int
	err := d.db.QueryRowContext(ctx,
		`INSERT INTO memory_entries (category, key, content, source, importance)
		 VALUES ($1, $2, $3, $4, $5)
		 RETURNING id`,
		category, key, content, source, importance,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("save memory entry: %w", err)
	}
	return id, nil
}

// UpdateMemoryEntry updates the content and importance of an existing entry.
// Returns ErrNotFound if the entry does not exist.
func (d *DB) UpdateMemoryEntry(ctx context.Context, id int, content string, importance int) error {
	result, err := d.db.ExecContext(ctx,
		`UPDATE memory_entries SET content = $1, importance = $2, updated_at = NOW()
		 WHERE id = $3`,
		content, importance, id,
	)
	if err != nil {
		return fmt.Errorf("update memory entry: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("update memory entry rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("update memory entry %d: %w", id, ErrNotFound)
	}
	return nil
}

// DeactivateMemoryEntry soft-deletes an entry by ID.
// Returns ErrNotFound if the entry does not exist.
func (d *DB) DeactivateMemoryEntry(ctx context.Context, id int) error {
	result, err := d.db.ExecContext(ctx,
		`UPDATE memory_entries SET is_active = FALSE, updated_at = NOW()
		 WHERE id = $1`,
		id,
	)
	if err != nil {
		return fmt.Errorf("deactivate memory entry: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("deactivate memory entry rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("deactivate memory entry %d: %w", id, ErrNotFound)
	}
	return nil
}

// DeactivateByContent soft-deletes entries matching the content fragment (case-insensitive).
// Returns the number of entries deactivated.
// NOTE: Uses PostgreSQL ILIKE — '%' and '_' in contentFragment act as wildcards.
// Rejects fragments that are only wildcards to prevent accidental mass deactivation.
func (d *DB) DeactivateByContent(ctx context.Context, contentFragment string) (int64, error) {
	// Reject fragments that consist solely of ILIKE wildcard characters.
	// e.g., "%", "_", "%%", "%_%" would match ALL active entries.
	stripped := strings.ReplaceAll(strings.ReplaceAll(contentFragment, "%", ""), "_", "")
	if stripped == "" {
		return 0, fmt.Errorf("deactivate by content: fragment must contain non-wildcard characters")
	}
	result, err := d.db.ExecContext(ctx,
		`UPDATE memory_entries SET is_active = FALSE, updated_at = NOW()
		 WHERE is_active = TRUE AND content ILIKE '%' || $1 || '%'`,
		contentFragment,
	)
	if err != nil {
		return 0, fmt.Errorf("deactivate by content: %w", err)
	}
	return result.RowsAffected()
}

// GetActiveMemory returns all active entries ordered by category priority and importance.
func (d *DB) GetActiveMemory(ctx context.Context) ([]MemoryEntry, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT id, category, COALESCE(key, ''), content, importance, source, is_active, created_at, updated_at
		 FROM memory_entries
		 WHERE is_active = TRUE
		 ORDER BY
		   CASE category
		     WHEN 'self' THEN 0
		     WHEN 'bootstrap' THEN 1
		     WHEN 'preference' THEN 2
		     WHEN 'fact' THEN 3
		   END,
		   importance DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("get active memory: %w", err)
	}
	defer rows.Close()
	return scanMemoryEntries(rows)
}

// GetMemoryByCategory returns active entries for a specific category.
func (d *DB) GetMemoryByCategory(ctx context.Context, category string) ([]MemoryEntry, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT id, category, COALESCE(key, ''), content, importance, source, is_active, created_at, updated_at
		 FROM memory_entries
		 WHERE is_active = TRUE AND category = $1
		 ORDER BY importance DESC`,
		category,
	)
	if err != nil {
		return nil, fmt.Errorf("get memory by category: %w", err)
	}
	defer rows.Close()
	return scanMemoryEntries(rows)
}

// GetMemoryByKey returns a single active entry matching the given key.
func (d *DB) GetMemoryByKey(ctx context.Context, key string) (*MemoryEntry, error) {
	var m MemoryEntry
	err := d.db.QueryRowContext(ctx,
		`SELECT id, category, COALESCE(key, ''), content, importance, source, is_active, created_at, updated_at
		 FROM memory_entries
		 WHERE is_active = TRUE AND key = $1
		 LIMIT 1`,
		key,
	).Scan(&m.ID, &m.Category, &m.Key, &m.Content, &m.Importance, &m.Source, &m.IsActive, &m.CreatedAt, &m.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get memory by key: %w", err)
	}
	return &m, nil
}

// CountAutoFacts returns the number of active auto-extracted facts.
func (d *DB) CountAutoFacts(ctx context.Context) (int, error) {
	var count int
	err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM memory_entries WHERE is_active = TRUE AND source = 'auto'`,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count auto facts: %w", err)
	}
	return count, nil
}

// CountActiveMemory returns the total number of active memory entries.
func (d *DB) CountActiveMemory(ctx context.Context) (int, error) {
	var count int
	err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM memory_entries WHERE is_active = TRUE`,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count active memory: %w", err)
	}
	return count, nil
}

// PruneAutoFacts deactivates the lowest-importance auto-extracted facts,
// keeping at most keepMax active auto-facts.
// The subquery orders by importance DESC so that OFFSET skips the top-importance
// entries (keeping them) and returns the low-importance tail for deactivation.
func (d *DB) PruneAutoFacts(ctx context.Context, keepMax int) (int64, error) {
	if keepMax <= 0 {
		return 0, fmt.Errorf("prune auto facts: keepMax must be positive, got %d", keepMax)
	}
	result, err := d.db.ExecContext(ctx,
		`UPDATE memory_entries SET is_active = FALSE, updated_at = NOW()
		 WHERE id IN (
		   SELECT id FROM memory_entries
		   WHERE is_active = TRUE AND source = 'auto'
		   ORDER BY importance DESC, created_at DESC
		   OFFSET $1
		 )`,
		keepMax,
	)
	if err != nil {
		return 0, fmt.Errorf("prune auto facts: %w", err)
	}
	return result.RowsAffected()
}

func scanMemoryEntries(rows *sql.Rows) ([]MemoryEntry, error) {
	var entries []MemoryEntry
	for rows.Next() {
		var m MemoryEntry
		if err := rows.Scan(&m.ID, &m.Category, &m.Key, &m.Content, &m.Importance, &m.Source, &m.IsActive, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning memory entry: %w", err)
		}
		entries = append(entries, m)
	}
	return entries, rows.Err()
}

// ================================================================
// People
// ================================================================

// SavePerson creates a new person with optional embedding. Returns the new person's ID.
func (d *DB) SavePerson(ctx context.Context, p *Person, embedding []float32) (int, error) {
	if p == nil {
		return 0, fmt.Errorf("save person: person is nil")
	}
	var embStr *string
	if len(embedding) > 0 {
		s := vectorToString(embedding)
		embStr = &s
	}
	var id int
	err := d.db.QueryRowContext(ctx,
		`INSERT INTO people (full_name, preferred_name, type, role, company, notes, embedding)
		 VALUES ($1, $2, $3, $4, $5, $6, $7::vector)
		 RETURNING id`,
		p.FullName, nullString(p.PreferredName), p.Type, nullString(p.Role), nullString(p.Company), nullString(p.Notes), embStr,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("save person: %w", err)
	}
	return id, nil
}

// UpdatePerson updates an existing person's details and embedding.
// Returns ErrNotFound if the person does not exist.
func (d *DB) UpdatePerson(ctx context.Context, id int, p *Person, embedding []float32) error {
	if p == nil {
		return fmt.Errorf("update person: person is nil")
	}
	var embStr *string
	if len(embedding) > 0 {
		s := vectorToString(embedding)
		embStr = &s
	}
	result, err := d.db.ExecContext(ctx,
		`UPDATE people SET full_name = $1, preferred_name = $2, type = $3, role = $4,
		        company = $5, notes = $6, embedding = $7::vector, updated_at = NOW()
		 WHERE id = $8`,
		p.FullName, nullString(p.PreferredName), p.Type, nullString(p.Role),
		nullString(p.Company), nullString(p.Notes), embStr, id,
	)
	if err != nil {
		return fmt.Errorf("update person: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("update person rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("update person %d: %w", id, ErrNotFound)
	}
	return nil
}

// DeletePerson soft-deletes a person by ID.
// Returns ErrNotFound if the person does not exist.
func (d *DB) DeletePerson(ctx context.Context, id int) error {
	result, err := d.db.ExecContext(ctx,
		`UPDATE people SET is_active = FALSE, updated_at = NOW() WHERE id = $1`,
		id,
	)
	if err != nil {
		return fmt.Errorf("delete person: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete person rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("delete person %d: %w", id, ErrNotFound)
	}
	return nil
}

// scanPerson scans the standard 10 person columns from a row.
// Matches the SELECT: id, full_name, COALESCE(preferred_name, ”), type,
// COALESCE(role, ”), COALESCE(company, ”), COALESCE(notes, ”),
// is_active, created_at, updated_at.
func scanPerson(scanner interface{ Scan(...any) error }) (Person, error) {
	var p Person
	err := scanner.Scan(&p.ID, &p.FullName, &p.PreferredName, &p.Type, &p.Role, &p.Company, &p.Notes, &p.IsActive, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

// GetPersonByID returns a single active person by primary key.
func (d *DB) GetPersonByID(ctx context.Context, id int) (*PersonWithContacts, error) {
	row := d.db.QueryRowContext(ctx,
		`SELECT id, full_name, COALESCE(preferred_name, ''), type, COALESCE(role, ''),
		        COALESCE(company, ''), COALESCE(notes, ''), is_active, created_at, updated_at
		 FROM people
		 WHERE id = $1 AND is_active = TRUE`,
		id,
	)
	p, err := scanPerson(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get person by id: %w", err)
	}

	contacts, err := d.getContacts(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	return &PersonWithContacts{Person: p, Contacts: contacts}, nil
}

// FindPersonByName finds the best-matching active person by name (trigram similarity > 0.3).
func (d *DB) FindPersonByName(ctx context.Context, name string) (*PersonWithContacts, error) {
	row := d.db.QueryRowContext(ctx,
		`SELECT id, full_name, COALESCE(preferred_name, ''), type, COALESCE(role, ''),
		        COALESCE(company, ''), COALESCE(notes, ''), is_active, created_at, updated_at
		 FROM people
		 WHERE is_active = TRUE
		   AND (similarity(full_name, $1) > 0.3 OR similarity(COALESCE(preferred_name, ''), $1) > 0.3)
		 ORDER BY GREATEST(similarity(full_name, $1), similarity(COALESCE(preferred_name, ''), $1)) DESC
		 LIMIT 1`,
		name,
	)
	p, err := scanPerson(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find person by name: %w", err)
	}

	contacts, err := d.getContacts(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	return &PersonWithContacts{Person: p, Contacts: contacts}, nil
}

// FindPersonByEmbedding finds the best-matching active person by semantic similarity.
func (d *DB) FindPersonByEmbedding(ctx context.Context, embedding []float32, minSimilarity float64) (*PersonWithContacts, error) {
	if len(embedding) == 0 {
		return nil, nil
	}
	embStr := vectorToString(embedding)

	var p Person
	var similarity float64
	err := d.db.QueryRowContext(ctx,
		`SELECT id, full_name, COALESCE(preferred_name, ''), type, COALESCE(role, ''),
		        COALESCE(company, ''), COALESCE(notes, ''), is_active, created_at, updated_at,
		        1 - (embedding <=> $1::vector) AS similarity
		 FROM people
		 WHERE is_active = TRUE AND embedding IS NOT NULL
		 ORDER BY embedding <=> $1::vector
		 LIMIT 1`,
		embStr,
	).Scan(&p.ID, &p.FullName, &p.PreferredName, &p.Type, &p.Role, &p.Company, &p.Notes, &p.IsActive, &p.CreatedAt, &p.UpdatedAt, &similarity)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find person by embedding: %w", err)
	}
	if similarity < minSimilarity {
		return nil, nil
	}

	contacts, err := d.getContacts(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	return &PersonWithContacts{Person: p, Contacts: contacts}, nil
}

// ListPeople returns all active people with their contacts.
// Uses a single JOIN query to avoid N+1 query overhead.
func (d *DB) ListPeople(ctx context.Context) ([]PersonWithContacts, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT p.id, p.full_name, COALESCE(p.preferred_name, ''), p.type, COALESCE(p.role, ''),
		        COALESCE(p.company, ''), COALESCE(p.notes, ''), p.is_active, p.created_at, p.updated_at,
		        c.id, c.person_id, c.contact_type, c.value, COALESCE(c.label, ''), c.is_primary
		 FROM people p
		 LEFT JOIN contact_details c ON c.person_id = p.id
		 WHERE p.is_active = TRUE
		 ORDER BY p.full_name, c.is_primary DESC, c.id`,
	)
	if err != nil {
		return nil, fmt.Errorf("list people: %w", err)
	}
	defer rows.Close()

	peopleMap := make(map[int]*PersonWithContacts)
	var order []int
	for rows.Next() {
		var p Person
		var cID, cPersonID sql.NullInt64
		var cType, cValue, cLabel sql.NullString
		var cPrimary sql.NullBool
		if err := rows.Scan(&p.ID, &p.FullName, &p.PreferredName, &p.Type, &p.Role, &p.Company, &p.Notes, &p.IsActive, &p.CreatedAt, &p.UpdatedAt,
			&cID, &cPersonID, &cType, &cValue, &cLabel, &cPrimary); err != nil {
			return nil, fmt.Errorf("scanning person: %w", err)
		}
		if _, exists := peopleMap[p.ID]; !exists {
			peopleMap[p.ID] = &PersonWithContacts{Person: p}
			order = append(order, p.ID)
		}
		if cID.Valid {
			peopleMap[p.ID].Contacts = append(peopleMap[p.ID].Contacts, ContactDetail{
				ID:          int(cID.Int64),
				PersonID:    int(cPersonID.Int64),
				ContactType: cType.String,
				Value:       cValue.String,
				Label:       cLabel.String,
				IsPrimary:   cPrimary.Bool,
			})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	result := make([]PersonWithContacts, 0, len(order))
	for _, id := range order {
		result = append(result, *peopleMap[id])
	}
	return result, nil
}

// SaveContactDetail adds a contact detail to a person.
func (d *DB) SaveContactDetail(ctx context.Context, personID int, contactType, value, label string) error {
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO contact_details (person_id, contact_type, value, label)
		 VALUES ($1, $2, $3, $4)`,
		personID, contactType, value, nullString(label),
	)
	if err != nil {
		return fmt.Errorf("save contact detail: %w", err)
	}
	return nil
}

func (d *DB) getContacts(ctx context.Context, personID int) ([]ContactDetail, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT id, person_id, contact_type, value, COALESCE(label, ''), is_primary
		 FROM contact_details
		 WHERE person_id = $1
		 ORDER BY is_primary DESC, id`,
		personID,
	)
	if err != nil {
		return nil, fmt.Errorf("get contacts: %w", err)
	}
	defer rows.Close()

	var contacts []ContactDetail
	for rows.Next() {
		var c ContactDetail
		if err := rows.Scan(&c.ID, &c.PersonID, &c.ContactType, &c.Value, &c.Label, &c.IsPrimary); err != nil {
			return nil, fmt.Errorf("scanning contact: %w", err)
		}
		contacts = append(contacts, c)
	}
	return contacts, rows.Err()
}

// ================================================================
// Daily Journal
// ================================================================

// UpsertJournalEntry creates or updates a journal entry for a date.
func (d *DB) UpsertJournalEntry(ctx context.Context, date string, entry *JournalEntry, embedding []float32) error {
	if entry == nil {
		return fmt.Errorf("upsert journal entry: entry is nil")
	}
	var embStr *string
	if len(embedding) > 0 {
		s := vectorToString(embedding)
		embStr = &s
	}
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO daily_journal (date, summary, decisions, actions, topics, projects, stats, embedding)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8::vector)
		 ON CONFLICT (date) DO UPDATE SET
		   summary = EXCLUDED.summary,
		   decisions = EXCLUDED.decisions,
		   actions = EXCLUDED.actions,
		   topics = EXCLUDED.topics,
		   projects = EXCLUDED.projects,
		   stats = EXCLUDED.stats,
		   embedding = EXCLUDED.embedding`,
		date, entry.Summary, entry.Decisions, entry.Actions, entry.Topics, entry.Projects, entry.Stats, embStr,
	)
	if err != nil {
		return fmt.Errorf("upsert journal entry: %w", err)
	}
	return nil
}

// GetJournalEntry returns the journal entry for a specific date (YYYY-MM-DD).
func (d *DB) GetJournalEntry(ctx context.Context, date string) (*JournalEntry, error) {
	var e JournalEntry
	err := d.db.QueryRowContext(ctx,
		`SELECT id, date, summary, decisions, actions, topics, projects, stats, created_at
		 FROM daily_journal
		 WHERE date = $1`,
		date,
	).Scan(&e.ID, &e.Date, &e.Summary, &e.Decisions, &e.Actions, &e.Topics, &e.Projects, &e.Stats, &e.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get journal entry: %w", err)
	}
	return &e, nil
}

// GetRecentJournalEntries returns the last N journal entries.
func (d *DB) GetRecentJournalEntries(ctx context.Context, days int) ([]JournalEntry, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT id, date, summary, decisions, actions, topics, projects, stats, created_at
		 FROM daily_journal
		 ORDER BY date DESC
		 LIMIT $1`,
		days,
	)
	if err != nil {
		return nil, fmt.Errorf("get recent journal entries: %w", err)
	}
	defer rows.Close()

	var entries []JournalEntry
	for rows.Next() {
		var e JournalEntry
		if err := rows.Scan(&e.ID, &e.Date, &e.Summary, &e.Decisions, &e.Actions, &e.Topics, &e.Projects, &e.Stats, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning journal entry: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// SearchJournal finds journal entries semantically similar to the given embedding.
func (d *DB) SearchJournal(ctx context.Context, embedding []float32, limit int, minSimilarity float64) ([]JournalEntry, error) {
	if len(embedding) == 0 {
		return nil, nil
	}
	embStr := vectorToString(embedding)

	rows, err := d.db.QueryContext(ctx,
		`SELECT id, date, summary, decisions, actions, topics, projects, stats, created_at
		 FROM daily_journal
		 WHERE embedding IS NOT NULL
		   AND 1 - (embedding <=> $1::vector) >= $3
		 ORDER BY embedding <=> $1::vector
		 LIMIT $2`,
		embStr, limit, minSimilarity,
	)
	if err != nil {
		return nil, fmt.Errorf("search journal: %w", err)
	}
	defer rows.Close()

	var entries []JournalEntry
	for rows.Next() {
		var e JournalEntry
		if err := rows.Scan(&e.ID, &e.Date, &e.Summary, &e.Decisions, &e.Actions, &e.Topics, &e.Projects, &e.Stats, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning journal entry: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// ================================================================
// User Profile
// ================================================================

// GetAllProfileAspects returns all profile aspects ordered by confidence descending.
func (d *DB) GetAllProfileAspects(ctx context.Context) ([]ProfileAspect, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT id, aspect, content, evidence, confidence, updated_at
		 FROM user_profile
		 ORDER BY confidence DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("get all profile aspects: %w", err)
	}
	defer rows.Close()

	var aspects []ProfileAspect
	for rows.Next() {
		var a ProfileAspect
		if err := rows.Scan(&a.ID, &a.Aspect, &a.Content, &a.Evidence, &a.Confidence, &a.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning profile aspect: %w", err)
		}
		aspects = append(aspects, a)
	}
	return aspects, rows.Err()
}

// GetProfileAspect returns a single profile aspect by name.
func (d *DB) GetProfileAspect(ctx context.Context, aspect string) (*ProfileAspect, error) {
	var a ProfileAspect
	err := d.db.QueryRowContext(ctx,
		`SELECT id, aspect, content, evidence, confidence, updated_at
		 FROM user_profile
		 WHERE aspect = $1`,
		aspect,
	).Scan(&a.ID, &a.Aspect, &a.Content, &a.Evidence, &a.Confidence, &a.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get profile aspect: %w", err)
	}
	return &a, nil
}

// UpsertProfileAspect creates or updates a profile aspect.
func (d *DB) UpsertProfileAspect(ctx context.Context, aspect, content string, evidence json.RawMessage, confidence float64) error {
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO user_profile (aspect, content, evidence, confidence)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (aspect) DO UPDATE SET
		   content = EXCLUDED.content,
		   evidence = EXCLUDED.evidence,
		   confidence = EXCLUDED.confidence,
		   updated_at = NOW()`,
		aspect, content, evidence, confidence,
	)
	if err != nil {
		return fmt.Errorf("upsert profile aspect: %w", err)
	}
	return nil
}

// DeleteProfileAspect removes a profile aspect.
// Returns ErrNotFound if the aspect does not exist.
func (d *DB) DeleteProfileAspect(ctx context.Context, aspect string) error {
	result, err := d.db.ExecContext(ctx,
		`DELETE FROM user_profile WHERE aspect = $1`,
		aspect,
	)
	if err != nil {
		return fmt.Errorf("delete profile aspect: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete profile aspect rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("delete profile aspect %q: %w", aspect, ErrNotFound)
	}
	return nil
}

// ================================================================
// Helpers
// ================================================================

// nullString returns nil for empty strings, otherwise a pointer to the string.
func nullString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
