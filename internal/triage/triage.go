// Package triage provides local LLM pre-processing for message classification
// and fact extraction using a 7B model via Ollama.
package triage

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/you/claude-gateway/internal/ollama"
)

// Triager classifies incoming messages and decides whether they can be handled
// locally by the 7B model or need Claude.
type Triager struct {
	llm *ollama.Client
}

// Analysis is the result of pre-processing a message.
type Analysis struct {
	CanHandleLocally bool
	LocalResponse    string
	PeopleNames      []string
	Topics           []string
}

// NewTriager creates a new triager.
func NewTriager(llm *ollama.Client) *Triager {
	return &Triager{llm: llm}
}

// greetingResponses maps common greeting patterns to local responses.
// These bypass the LLM entirely for instant, zero-cost handling.
//
// IMPORTANT: Do NOT add acknowledgements or affirmatives here (ok, sure,
// yes, cool, nice, etc.). These are almost always follow-ups to a previous
// bot question ("Shall I do X?" → "ok") and MUST reach Claude with context.
var greetingResponses = map[string]string{
	"hi":             "Hi! How can I help you today?",
	"hey":            "Hey! What can I do for you?",
	"hello":          "Hello! How can I help?",
	"good morning":   "Good morning! How can I help you today?",
	"good afternoon": "Good afternoon! What can I do for you?",
	"good evening":   "Good evening! How can I help?",
	"morning":        "Good morning! How can I help you today?",
	"thanks":         "You're welcome! Let me know if you need anything else.",
	"thank you":      "You're welcome! Let me know if there's anything else I can help with.",
	"cheers":         "Cheers! Let me know if you need anything.",
	"bye":            "Goodbye! Have a great day.",
	"goodbye":        "Goodbye! Feel free to come back anytime.",
	"yo":             "Hey! What can I do for you?",
	"howdy":          "Howdy! How can I help?",
}

// isAffirmative returns true for short confirmations/affirmatives that are
// almost certainly follow-ups to a previous bot question. These MUST reach
// Claude so it can see the conversational context and continue the task.
func isAffirmative(normalized string) bool {
	affirmatives := []string{
		"yes", "yep", "yeah", "yea", "ya", "yup",
		"yes pls", "yes please", "yes do it", "yes go ahead",
		"sure", "sure thing",
		"do it", "go ahead", "go for it", "crack on", "yes crack on",
		"proceed", "continue", "carry on",
		"ok", "okay", "k", "kk",
		"cool", "nice", "great", "perfect", "sounds good",
		"go on", "get on with it", "just do it",
		"please do", "pls do", "please", "pls",
		"absolutely", "definitely", "of course",
	}
	for _, a := range affirmatives {
		if normalized == a {
			return true
		}
	}
	return false
}

// matchGreeting checks if the message is a simple greeting.
// Returns the response and true if matched, empty string and false otherwise.
// Affirmatives (yes, ok, sure, do it) are explicitly excluded — they are
// follow-ups to the bot's previous question and must reach Claude.
func matchGreeting(message string) (string, bool) {
	normalized := strings.TrimSpace(strings.ToLower(message))
	// Strip common punctuation from end
	normalized = strings.TrimRight(normalized, "!?.,")
	normalized = strings.TrimSpace(normalized)

	// Never intercept affirmatives — they're task continuations
	if isAffirmative(normalized) {
		return "", false
	}

	if resp, ok := greetingResponses[normalized]; ok {
		return resp, true
	}
	return "", false
}

const triageSystemPrompt = `You classify messages and extract metadata. Return JSON only.

EXAMPLES:

User says: "hi there"
{"can_handle_locally": true, "local_response": "Hi! How can I help you today?", "people_names": [], "topics": []}

User says: "good morning!"
{"can_handle_locally": true, "local_response": "Good morning! How can I help you today?", "people_names": [], "topics": []}

User says: "thanks for that"
{"can_handle_locally": true, "local_response": "You're welcome! Let me know if you need anything else.", "people_names": [], "topics": []}

User says: "Can you review the auth middleware?"
{"can_handle_locally": false, "local_response": "", "people_names": [], "topics": ["auth", "middleware", "code review"]}

User says: "Draft an email to Sarah about the invoice"
{"can_handle_locally": false, "local_response": "", "people_names": ["Sarah"], "topics": ["email", "invoice"]}

RULES:
- can_handle_locally=true ONLY for: greetings (hi, hello, good morning) and thanks (thanks, thank you, cheers)
- can_handle_locally=false for: EVERYTHING ELSE — questions, requests, code, analysis, anything requiring thought
- CRITICAL: short affirmatives (yes, ok, sure, do it, go ahead, carry on, crack on, please, pls) are NEVER locally handleable — they are follow-up instructions to a previous conversation and MUST reach the main assistant
- When can_handle_locally=true, ALWAYS provide a friendly local_response
- When can_handle_locally=false, set local_response to empty string ""
- Extract people_names: any person names mentioned
- Extract topics: key subjects discussed
- Return valid JSON only, no other text`

// triageResponse matches the JSON output from the 7B model.
type triageResponse struct {
	CanHandleLocally bool     `json:"can_handle_locally"`
	LocalResponse    *string  `json:"local_response"`
	PeopleNames      []string `json:"people_names"`
	Topics           []string `json:"topics"`
}

// Analyze classifies a message using the local 7B model.
// On any error, returns a safe fallback that passes to Claude.
// The error return is always nil — all failures produce a safe fallback.
// Retained for API forward-compatibility (future callers may want to distinguish
// "successfully classified as pass-through" from "classification failed").
func (t *Triager) Analyze(ctx context.Context, message string) (*Analysis, error) {
	// Fast-path: hardcoded greetings bypass the LLM entirely.
	if resp, ok := matchGreeting(message); ok {
		log.Printf("Triage: fast-path greeting match for %q", message)
		return &Analysis{
			CanHandleLocally: true,
			LocalResponse:    resp,
		}, nil
	}

	if t.llm == nil {
		return &Analysis{CanHandleLocally: false}, nil
	}

	// Truncate to avoid overwhelming the 7B model with very large messages.
	if len(message) > 4000 {
		message = message[:4000]
	}

	var resp triageResponse
	err := t.llm.GenerateJSONWithOpts(ctx, message, triageSystemPrompt, &resp, ollama.FastClassifyOpts)
	if err != nil {
		log.Printf("Triage analysis failed (passing to Claude): %v", err)
		return &Analysis{CanHandleLocally: false}, nil
	}

	analysis := &Analysis{
		CanHandleLocally: resp.CanHandleLocally,
		PeopleNames:      resp.PeopleNames,
		Topics:           resp.Topics,
	}
	if resp.LocalResponse != nil {
		analysis.LocalResponse = *resp.LocalResponse
	}

	log.Printf("Triage LLM result: can_handle_locally=%v, local_response=%q, people=%v, topics=%v",
		resp.CanHandleLocally, analysis.LocalResponse, resp.PeopleNames, resp.Topics)

	// Safety: if local handling claimed but no response provided, pass to Claude
	if analysis.CanHandleLocally && analysis.LocalResponse == "" {
		log.Printf("Triage: flipped to CLAUDE — local handling claimed but no response provided")
		analysis.CanHandleLocally = false
	}

	// Safety: never locally handle affirmatives, even if the LLM says so.
	// These are task continuations ("yes", "do it", "go ahead") responding
	// to a previous bot question. The 7B model has no conversation context
	// and cannot distinguish "ok" (greeting) from "ok" (proceed with task).
	if analysis.CanHandleLocally {
		normalized := strings.TrimSpace(strings.ToLower(message))
		normalized = strings.TrimRight(normalized, "!?.,")
		normalized = strings.TrimSpace(normalized)
		if isAffirmative(normalized) {
			log.Printf("Triage: flipped to CLAUDE — affirmative %q is a task continuation, not a greeting", message)
			analysis.CanHandleLocally = false
			analysis.LocalResponse = ""
		}
	}

	return analysis, nil
}

// IsAvailable returns true if the LLM client is configured.
func (t *Triager) IsAvailable() bool {
	return t.llm != nil
}

// FormatAnalysis returns a human-readable summary of the analysis (for logging).
func FormatAnalysis(a *Analysis) string {
	if a == nil {
		return "nil"
	}
	if a.CanHandleLocally {
		return fmt.Sprintf("LOCAL (people=%v, topics=%v)", a.PeopleNames, a.Topics)
	}
	return fmt.Sprintf("CLAUDE (people=%v, topics=%v)", a.PeopleNames, a.Topics)
}
