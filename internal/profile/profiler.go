// Package profile manages user profiling — accumulating observations about
// communication style, work patterns, priorities, and preferences over time.
package profile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/you/claude-gateway/internal/journal"
	"github.com/you/claude-gateway/internal/ollama"
	"github.com/you/claude-gateway/internal/pgdb"
)

// Profiler manages user profile observations and aspects.
type Profiler struct {
	db  *pgdb.DB
	llm *ollama.Client
	// mu serializes applyObservation to prevent TOCTOU race when concurrent
	// handlers read-then-write the same aspect.
	mu sync.Mutex
}

// Evidence represents a single observation that supports a profile aspect.
type Evidence struct {
	Observation string `json:"observation"`
	Date        string `json:"date"`
	Source      string `json:"source"` // "conversation", "explicit", "journal"
}

// NewProfiler creates a new profiler. Panics if db is nil because
// every Profiler method depends on a valid database connection.
func NewProfiler(db *pgdb.DB, llm *ollama.Client) *Profiler {
	if db == nil {
		panic("profile: nil db")
	}
	return &Profiler{db: db, llm: llm}
}

// GetFullProfile returns all profile aspects sorted by confidence descending.
func (p *Profiler) GetFullProfile(ctx context.Context) ([]pgdb.ProfileAspect, error) {
	return p.db.GetAllProfileAspects(ctx)
}

// GetAspect returns a single profile aspect.
func (p *Profiler) GetAspect(ctx context.Context, aspect string) (*pgdb.ProfileAspect, error) {
	return p.db.GetProfileAspect(ctx, aspect)
}

// SetAspect explicitly sets a profile aspect (user override). Confidence = 1.0.
func (p *Profiler) SetAspect(ctx context.Context, aspect, content string) error {
	evidence := []Evidence{{
		Observation: "Explicitly set by user",
		Date:        time.Now().Format("2006-01-02"),
		Source:      "explicit",
	}}
	evJSON, err := json.Marshal(evidence)
	if err != nil {
		return fmt.Errorf("marshal evidence: %w", err)
	}
	return p.db.UpsertProfileAspect(ctx, aspect, content, evJSON, 1.0)
}

// DeleteAspect removes a profile aspect entirely.
func (p *Profiler) DeleteAspect(ctx context.Context, aspect string) error {
	return p.db.DeleteProfileAspect(ctx, aspect)
}

const profileObservationPrompt = `Given this conversation exchange, identify observations about the user's:
- communication_style: how they phrase requests
- query_style: how they approach Claude (brief vs detailed, technical level)
- priorities: what seems important to them right now
- work_patterns: any timing/rhythm observations
- email_style: tone, formality, structure preferences
- tools_preferences: preferred tools, languages, approaches

Return JSON:
{
  "observations": [
    {"aspect": "query_style", "observation": "Uses concise imperative sentences", "confidence_delta": 0.05}
  ]
}

Only include observations clearly supported by this exchange.
Return empty observations if nothing notable.
confidence_delta should be 0.03-0.08 (small increments).
Return valid JSON only, no markdown fences.`

// observationResponse matches the JSON output from the 7B model.
type observationResponse struct {
	Observations []struct {
		Aspect          string  `json:"aspect"`
		Observation     string  `json:"observation"`
		ConfidenceDelta float64 `json:"confidence_delta"`
	} `json:"observations"`
}

// UpdateFromConversation analyzes an exchange and updates profile observations.
func (p *Profiler) UpdateFromConversation(ctx context.Context, userMessage, assistantResponse string) error {
	if p.llm == nil {
		return nil
	}

	prompt := "USER MESSAGE:\n" + userMessage + "\n\nASSISTANT RESPONSE:\n" + assistantResponse

	var resp observationResponse
	err := p.llm.GenerateJSON(ctx, prompt, profileObservationPrompt, &resp)
	if err != nil {
		return fmt.Errorf("profile observation: %w", err)
	}

	today := time.Now().Format("2006-01-02")

	var errs []error
	for _, obs := range resp.Observations {
		if obs.Aspect == "" || obs.Observation == "" {
			continue
		}

		// Clamp confidence delta
		if obs.ConfidenceDelta < 0.01 {
			obs.ConfidenceDelta = 0.03
		}
		if obs.ConfidenceDelta > 0.1 {
			obs.ConfidenceDelta = 0.08
		}

		if err := p.applyObservation(ctx, obs.Aspect, obs.Observation, obs.ConfidenceDelta, today, "conversation"); err != nil {
			log.Printf("Profile apply observation failed: %v", err)
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

// UpdateFromJournal analyzes a daily journal entry for profile patterns.
func (p *Profiler) UpdateFromJournal(ctx context.Context, entry *journal.DayEntry) error {
	if p.llm == nil || entry == nil {
		return nil
	}

	// Detect project focus patterns
	if len(entry.Projects) > 0 {
		projectList := strings.Join(entry.Projects, ", ")
		if err := p.applyObservation(ctx, "projects_focus", "Active projects: "+projectList, 0.05, entry.Date, "journal"); err != nil {
			log.Printf("Profile journal update failed (non-fatal): %v", err)
		}
	}

	// Detect recurring topics as priorities
	if len(entry.Topics) > 0 {
		topicList := strings.Join(entry.Topics, ", ")
		if err := p.applyObservation(ctx, "priorities", "Focus topics: "+topicList, 0.03, entry.Date, "journal"); err != nil {
			log.Printf("Profile journal update failed (non-fatal): %v", err)
		}
	}

	return nil
}

// FormatProfileForContext formats profile aspects for system prompt injection.
// Only includes aspects with confidence >= 0.4.
func (p *Profiler) FormatProfileForContext(ctx context.Context) (string, error) {
	aspects, err := p.db.GetAllProfileAspects(ctx)
	if err != nil {
		return "", fmt.Errorf("loading profile: %w", err)
	}

	var b strings.Builder
	included := 0
	for _, a := range aspects {
		if a.Confidence < 0.4 {
			continue
		}
		if included == 0 {
			b.WriteString("<user_profile>\n")
		}
		fmt.Fprintf(&b, "%s (confidence: %.2f): %s\n", formatAspectName(a.Aspect), a.Confidence, a.Content)
		included++
	}
	if included > 0 {
		b.WriteString("</user_profile>")
	}

	return b.String(), nil
}

// applyObservation updates a profile aspect with new evidence.
// Serialized by p.mu to prevent TOCTOU race on concurrent read-modify-write.
//
// NOTE: A single mutex serializes ALL aspect updates. This is acceptable for a
// single-user Telegram bot. If scaling to multiple concurrent users, switch to
// per-aspect locking or DB-level serialization (SELECT FOR UPDATE).
func (p *Profiler) applyObservation(ctx context.Context, aspect, observation string, confidenceDelta float64, date, source string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	existing, err := p.db.GetProfileAspect(ctx, aspect)
	if err != nil {
		return err
	}

	newEvidence := Evidence{
		Observation: observation,
		Date:        date,
		Source:      source,
	}

	if existing == nil {
		// Create new aspect with low confidence
		evidence := []Evidence{newEvidence}
		evJSON, err := json.Marshal(evidence)
		if err != nil {
			return fmt.Errorf("marshal evidence: %w", err)
		}
		return p.db.UpsertProfileAspect(ctx, aspect, observation, evJSON, 0.3)
	}

	// Check if this aspect was explicitly set by the user (pinned).
	// Explicitly-set aspects (confidence 1.0, source='explicit') must not be
	// degraded by auto-observations — the user's intent takes priority.
	var evidenceList []Evidence
	if err := json.Unmarshal(existing.Evidence, &evidenceList); err != nil {
		evidenceList = nil
	}
	if source != "explicit" {
		for _, ev := range evidenceList {
			if ev.Source == "explicit" {
				// Pinned aspect — skip auto-modification
				return nil
			}
		}
	}

	// Append evidence to existing
	evidenceList = append(evidenceList, newEvidence)

	// Keep last 20 evidence entries
	if len(evidenceList) > 20 {
		evidenceList = evidenceList[len(evidenceList)-20:]
	}

	evJSON, err := json.Marshal(evidenceList)
	if err != nil {
		return fmt.Errorf("marshal evidence: %w", err)
	}

	// Apply time-based confidence decay for non-explicit aspects (0.01/week)
	newConfidence := existing.Confidence
	if source != "explicit" {
		weeksSinceUpdate := time.Since(existing.UpdatedAt).Hours() / (24 * 7)
		if weeksSinceUpdate >= 1 {
			decay := 0.01 * weeksSinceUpdate
			newConfidence -= decay
			if newConfidence < 0.1 {
				newConfidence = 0.1
			}
		}
	}

	// Increase confidence (capped at 0.95 for auto-learned)
	newConfidence += confidenceDelta
	if newConfidence > 0.95 {
		newConfidence = 0.95
	}

	// Use latest observation as content (accumulated view)
	content := existing.Content
	if observation != "" {
		content = observation
	}

	return p.db.UpsertProfileAspect(ctx, aspect, content, evJSON, newConfidence)
}

// formatAspectName converts snake_case to Title Case.
// Handles doc: prefix for profile document aspects.
func formatAspectName(aspect string) string {
	if strings.HasPrefix(aspect, "doc:") {
		name := strings.TrimPrefix(aspect, "doc:")
		name = strings.NewReplacer("-", " ", "_", " ").Replace(name)
		parts := strings.Fields(name)
		for i, p := range parts {
			if len(p) > 0 {
				parts[i] = strings.ToUpper(p[:1]) + p[1:]
			}
		}
		return "[Doc] " + strings.Join(parts, " ")
	}
	parts := strings.Split(aspect, "_")
	for i, p := range parts {
		if len(p) > 0 {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, " ")
}
