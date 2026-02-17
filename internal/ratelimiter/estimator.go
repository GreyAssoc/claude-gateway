package ratelimiter

// EstimateInputTokens estimates the number of input tokens from text.
// Rough heuristic: ~4 characters per token for English text.
func EstimateInputTokens(text string) int {
	chars := len(text)
	if chars == 0 {
		return 0
	}
	return (chars + 3) / 4 // ceiling division
}

// EstimateRequestTokens estimates total input tokens for a request,
// including system prompt overhead.
func EstimateRequestTokens(promptText string, systemPromptSize int) int {
	promptTokens := EstimateInputTokens(promptText)
	systemTokens := systemPromptSize / 4 // system prompt chars to tokens
	return promptTokens + systemTokens
}
