package ratelimiter

import "strings"

// Context windows match sidecar/index.js CONTEXT_WINDOWS. A conversation is
// measured against its own model window. Agents on other models, and other
// conversations on the same model, do not share this number.
//
// Framework aliases cover the gateway's /models keys when the vendor model
// id is not known yet (the first admit, before the API echoes api_model).
const defaultContextWindow = 200_000

var contextWindows = map[string]int{
	// Claude
	"claude":                    200_000,
	"claude-sonnet-4-20250514":  200_000,
	"claude-sonnet-4-6":         200_000,
	"claude-haiku-4-5-20251001": 200_000,
	"claude-haiku-4-5":          200_000,
	"claude-opus-4-7":           200_000,
	"claude-opus-4-7[1m]":       1_000_000,
	"claude-opus-4-6":           200_000,
	// DeepSeek. Pro is 128k; Flash is 1M. They must not share a counter.
	"deepseek":          128_000,
	"deepseek-v4-pro":   128_000,
	"deepseek-chat":     128_000,
	"deepseek-flash":    1_000_000,
	"deepseek-v4-flash": 1_000_000,
	// Local Qwen (vLLM --max-model-len). Unlimited for admission; still reported.
	"qwen":        131_072,
	"ollama":      131_072,
	"vllm":        131_072,
	"qwen3.8":     131_072,
	"qwen3.8-27b": 131_072,
	// xAI Grok
	"grok":     2_000_000,
	"grok-4.6": 2_000_000,
	"grok-4.5": 2_000_000,
}

// ContextWindow returns the context size for a framework key and/or vendor
// model id. The model id wins. Unknown models use the framework alias, then
// the sidecar default of 200k.
func ContextWindow(framework, model string) int {
	model = strings.TrimSpace(model)
	framework = strings.TrimSpace(strings.ToLower(framework))
	if n, ok := lookupWindow(model); ok {
		return n
	}
	if n, ok := lookupWindow(framework); ok {
		return n
	}
	if strings.HasPrefix(strings.ToLower(model), "qwen3.8") || strings.HasPrefix(framework, "qwen") {
		return 131_072
	}
	return defaultContextWindow
}

func lookupWindow(name string) (int, bool) {
	if name == "" {
		return 0, false
	}
	if n, ok := contextWindows[name]; ok {
		return n, true
	}
	lower := strings.ToLower(name)
	if n, ok := contextWindows[lower]; ok {
		return n, true
	}
	if i := strings.LastIndex(name, "["); i > 0 && strings.HasSuffix(name, "]") {
		if n, ok := contextWindows[name[:i]]; ok {
			return n, true
		}
	}
	return 0, false
}

// Unlimited reports local frameworks that do not consume a paid context budget.
// Their turns are still stored on their own counter, and they are never queued
// because some other model filled a window.
func Unlimited(framework string) bool {
	switch strings.ToLower(strings.TrimSpace(framework)) {
	case "qwen", "ollama", "vllm", "qwen3.8", "qwen3.8-27b":
		return true
	default:
		return strings.HasPrefix(strings.ToLower(strings.TrimSpace(framework)), "qwen")
	}
}
