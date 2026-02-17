package memory

import (
	"log"
	"time"
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
func (c *Compactor) RunDailyCompaction() error {
	today := time.Now().Format("2006-01-02")
	log.Printf("Memory compaction: starting for %s", today)

	// Step 1: Build today's summary from session history
	if err := c.buildDailySummary(today); err != nil {
		log.Printf("Memory compaction: daily summary error: %v", err)
	}

	// Step 2: Prune old daily summaries (keep last 90 days)
	if err := c.pruneOldDailies(90); err != nil {
		log.Printf("Memory compaction: prune error: %v", err)
	}

	// Step 3: Prune low-importance curated memories (keep top 100)
	if err := c.pruneCuratedMemories(100); err != nil {
		log.Printf("Memory compaction: curated prune error: %v", err)
	}

	log.Printf("Memory compaction: complete for %s", today)
	return nil
}

func (c *Compactor) buildDailySummary(date string) error {
	// Count today's sessions
	var count int
	var totalInput, totalOutput int
	err := c.manager.db.QueryRow(
		`SELECT COUNT(*), COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0)
		 FROM session_history WHERE date(created_at) = ?`, date,
	).Scan(&count, &totalInput, &totalOutput)
	if err != nil {
		return err
	}

	if count == 0 {
		return nil // no sessions today
	}

	// Get a sample of prompts from today
	rows, err := c.manager.db.Query(
		`SELECT prompt_preview, source FROM session_history
		 WHERE date(created_at) = ? ORDER BY created_at LIMIT 20`, date,
	)
	if err != nil {
		return err
	}
	defer rows.Close()

	var topics []string
	for rows.Next() {
		var preview, source string
		if err := rows.Scan(&preview, &source); err != nil {
			continue
		}
		if preview != "" {
			topics = append(topics, preview)
		}
		_ = source
	}

	// Build simple summary without calling Claude (to avoid token cost)
	summary := buildSimpleSummary(count, totalInput, totalOutput, topics)

	return c.manager.SaveDailySummary(date, summary, "[]")
}

func buildSimpleSummary(count, inputTokens, outputTokens int, topics []string) string {
	summary := ""
	summary += formatCount(count) + " interactions. "
	summary += formatCount(inputTokens) + " input tokens, " + formatCount(outputTokens) + " output tokens. "

	if len(topics) > 0 {
		// Take first few topic previews
		maxTopics := 5
		if len(topics) < maxTopics {
			maxTopics = len(topics)
		}
		summary += "Topics: "
		for i := 0; i < maxTopics; i++ {
			if i > 0 {
				summary += "; "
			}
			t := topics[i]
			if len(t) > 50 {
				t = t[:50] + "..."
			}
			summary += t
		}
	}

	return summary
}

func formatCount(n int) string {
	if n >= 1_000_000 {
		return formatFloat(float64(n)/1_000_000) + "M"
	}
	if n >= 1_000 {
		return formatFloat(float64(n)/1_000) + "k"
	}
	return intToStr(n)
}

func formatFloat(f float64) string {
	s := intToStr(int(f * 10))
	if len(s) < 2 {
		return "0." + s
	}
	return s[:len(s)-1] + "." + s[len(s)-1:]
}

func intToStr(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	if neg {
		s = "-" + s
	}
	return s
}

func (c *Compactor) pruneOldDailies(keepDays int) error {
	cutoff := time.Now().AddDate(0, 0, -keepDays).Format("2006-01-02")
	_, err := c.manager.db.Exec("DELETE FROM memory_daily WHERE date < ?", cutoff)
	return err
}

func (c *Compactor) pruneCuratedMemories(maxKeep int) error {
	// Keep only the top N by importance
	_, err := c.manager.db.Exec(
		`DELETE FROM memory_curated WHERE id NOT IN (
			SELECT id FROM memory_curated ORDER BY importance DESC, created_at DESC LIMIT ?
		)`, maxKeep,
	)
	return err
}
