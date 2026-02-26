package memory

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/you/claude-gateway/internal/format"
)

// Compactor handles daily memory summarization and pruning.
type Compactor struct {
	manager *Manager
}

// NewCompactor creates a memory compactor.
func NewCompactor(manager *Manager) *Compactor {
	return &Compactor{manager: manager}
}

// RunDailyCompaction performs end-of-day memory operations:
// 1. Get today's session history and create a daily summary
// 2. Prune old daily summaries (compact into monthly)
// 3. Remove low-importance curated memories if over limit
func (c *Compactor) RunDailyCompaction(ctx context.Context) {
	today := time.Now().Format("2006-01-02")
	log.Printf("Memory compaction: starting for %s", today)

	// Step 1: Build today's summary from session history
	if err := c.buildDailySummary(ctx, today); err != nil {
		log.Printf("Memory compaction: daily summary error: %v", err)
	}

	// Step 2: Prune old daily summaries (keep last 90 days)
	if err := c.pruneOldDailies(ctx, 90); err != nil {
		log.Printf("Memory compaction: prune error: %v", err)
	}

	// Step 3: Prune low-importance curated memories (keep top 100)
	if err := c.pruneCuratedMemories(ctx, 100); err != nil {
		log.Printf("Memory compaction: curated prune error: %v", err)
	}

	log.Printf("Memory compaction: complete for %s", today)
}

func (c *Compactor) buildDailySummary(ctx context.Context, date string) error {
	// Count today's sessions
	var count int
	var totalInput, totalOutput int
	err := c.manager.db.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0)
		 FROM session_history WHERE date(created_at) = ?`, date,
	).Scan(&count, &totalInput, &totalOutput)
	if err != nil {
		return fmt.Errorf("querying session stats for %s: %w", date, err)
	}

	if count == 0 {
		return nil // no sessions today
	}

	// Get a sample of prompts from today
	rows, err := c.manager.db.QueryContext(ctx,
		`SELECT prompt_preview FROM session_history
		 WHERE date(created_at) = ? ORDER BY created_at LIMIT 20`, date,
	)
	if err != nil {
		return fmt.Errorf("querying session prompts for %s: %w", date, err)
	}
	defer rows.Close()

	var topics []string
	for rows.Next() {
		var preview string
		if err := rows.Scan(&preview); err != nil {
			log.Printf("Memory compaction: scan error: %v", err)
			continue
		}
		if preview != "" {
			topics = append(topics, preview)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	// Build simple summary without calling Claude (to avoid token cost)
	summary := buildSimpleSummary(count, totalInput, totalOutput, topics)

	return c.manager.SaveDailySummary(ctx, date, summary, "[]")
}

func buildSimpleSummary(count, inputTokens, outputTokens int, topics []string) string {
	var b strings.Builder
	b.WriteString(strconv.Itoa(count))
	b.WriteString(" interactions. ")
	b.WriteString(format.Tokens(inputTokens))
	b.WriteString(" input tokens, ")
	b.WriteString(format.Tokens(outputTokens))
	b.WriteString(" output tokens. ")

	if len(topics) > 0 {
		maxTopics := 5
		if len(topics) < maxTopics {
			maxTopics = len(topics)
		}
		b.WriteString("Topics: ")
		for i := 0; i < maxTopics; i++ {
			if i > 0 {
				b.WriteString("; ")
			}
			b.WriteString(format.Truncate(topics[i], 50))
		}
	}

	return b.String()
}

func (c *Compactor) pruneOldDailies(ctx context.Context, keepDays int) error {
	cutoff := time.Now().AddDate(0, 0, -keepDays).Format("2006-01-02")
	_, err := c.manager.db.ExecContext(ctx, "DELETE FROM memory_daily WHERE date < ?", cutoff)
	if err != nil {
		return fmt.Errorf("pruning daily summaries older than %s: %w", cutoff, err)
	}
	return nil
}

func (c *Compactor) pruneCuratedMemories(ctx context.Context, maxKeep int) error {
	// Keep only the top N by importance
	_, err := c.manager.db.ExecContext(ctx,
		`DELETE FROM memory_curated WHERE id NOT IN (
			SELECT id FROM memory_curated ORDER BY importance DESC, created_at DESC LIMIT ?
		)`, maxKeep,
	)
	if err != nil {
		return fmt.Errorf("pruning curated memories beyond %d: %w", maxKeep, err)
	}
	return nil
}
