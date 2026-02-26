package triage

import (
	"context"
	"log"
)

// ExtractedFact represents a fact extracted from a conversation exchange.
type ExtractedFact struct {
	Key        string
	Content    string
	Category   string // "fact", "preference", "decision"
	Importance int    // 1-10
}

const extractionSystemPrompt = `Extract facts worth remembering long-term from this conversation exchange. Return JSON:
{
  "facts": [
    { "key": "short_key", "content": "...", "category": "fact", "importance": 5 }
  ]
}

Rules:
- Only extract CONCRETE facts (names, decisions, preferences, deadlines, technical details)
- Do NOT extract vague observations or opinions
- Do NOT extract anything already obvious from context
- Do NOT extract the conversation itself as a fact
- category: "fact" (concrete information), "preference" (user preference), "decision" (decision made)
- importance: 1-3=trivial, 4-6=useful, 7-8=important, 9-10=critical
- Return empty facts array if nothing worth remembering
- Maximum 3 facts per exchange
- key should be a short snake_case identifier (e.g., "deployment_server_ip")

Return valid JSON only, no markdown fences.`

// extractionResponse matches the JSON output from the 7B model.
type extractionResponse struct {
	Facts []struct {
		Key        string `json:"key"`
		Content    string `json:"content"`
		Category   string `json:"category"`
		Importance int    `json:"importance"`
	} `json:"facts"`
}

// ExtractFacts analyzes a user/assistant exchange and extracts memorable facts.
// Returns an empty slice on any error (non-fatal).
// The error return is always nil — all failures degrade gracefully to an empty slice.
// Retained for API forward-compatibility.
func (t *Triager) ExtractFacts(ctx context.Context, userMessage, assistantResponse string) ([]ExtractedFact, error) {
	if t.llm == nil {
		return nil, nil
	}

	prompt := "USER MESSAGE:\n" + userMessage + "\n\nASSISTANT RESPONSE:\n" + assistantResponse
	// Truncate to avoid overwhelming the 7B model with very large exchanges.
	if len(prompt) > 8000 {
		prompt = prompt[:8000]
	}

	var resp extractionResponse
	err := t.llm.GenerateJSON(ctx, prompt, extractionSystemPrompt, &resp)
	if err != nil {
		log.Printf("Fact extraction failed (non-fatal): %v", err)
		return nil, nil
	}

	var facts []ExtractedFact
	for _, f := range resp.Facts {
		if f.Key == "" || f.Content == "" {
			continue
		}
		// Normalize category
		switch f.Category {
		case "fact", "preference", "decision":
			// valid
		default:
			f.Category = "fact"
		}
		// Clamp importance
		if f.Importance < 1 {
			f.Importance = 1
		}
		if f.Importance > 10 {
			f.Importance = 10
		}

		facts = append(facts, ExtractedFact{
			Key:        f.Key,
			Content:    f.Content,
			Category:   f.Category,
			Importance: f.Importance,
		})

		// Cap at 3 facts per exchange
		if len(facts) >= 3 {
			break
		}
	}

	return facts, nil
}
