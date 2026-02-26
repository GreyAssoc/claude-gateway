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
var greetingResponses = map[string]string{
	"hi":            "Hi! How can I help you today?",
	"hey":           "Hey! What can I do for you?",
	"hello":         "Hello! How can I help?",
	"good morning":  "Good morning! How can I help you today?",
	"good afternoon": "Good afternoon! What can I do for you?",
	"good evening":  "Good evening! How can I help?",
	"morning":       "Good morning! How can I help you today?",
	"thanks":        "You're welcome! Let me know if you need anything else.",
	"thank you":     "You're welcome! Let me know if there's anything else I can help with.",
	"cheers":        "Cheers! Let me know if you need anything.",
	"bye":           "Goodbye! Have a great day.",
	"goodbye":       "Goodbye! Feel free to come back anytime.",
	"ok":            "Got it! Let me know if you need anything.",
	"okay":          "Alright! Let me know if there's anything else.",
	"cool":          "Great! Let me know if you need anything else.",
	"nice":          "Glad to hear it! What else can I help with?",
	"yo":            "Hey! What can I do for you?",
	"howdy":         "Howdy! How can I help?",
}

// matchGreeting checks if the message is a simple greeting/acknowledgement.
// Returns the response and true if matched, empty string and false otherwise.
func matchGreeting(message string) (string, bool) {
	normalized := strings.TrimSpace(strings.ToLower(message))
	// Strip common punctuation from end
	normalized = strings.TrimRight(normalized, "!?.,")
	normalized = strings.TrimSpace(normalized)

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
- can_handle_locally=true ONLY for: greetings, thanks, small talk, brief acknowledgements
- can_handle_locally=false for: questions, requests, code, analysis, anything requiring thought
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
