// Package format provides text formatting utilities for display and truncation.
package format

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Tokens formats a token count for human display (e.g., "1.2k", "3.5M").
func Tokens(n int) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 1_000 {
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	}
	return strconv.Itoa(n)
}

// Truncate shortens a string to at most maxLen bytes, respecting UTF-8 boundaries.
// Appends "..." if truncation occurs (included in the maxLen budget).
// Returns s unchanged if maxLen <= 0 or s fits.
func Truncate(s string, maxLen int) string {
	if maxLen <= 0 || len(s) <= maxLen {
		return s
	}
	// Too short for ellipsis — hard-truncate at rune boundary
	if maxLen < 4 {
		end := maxLen
		for end > 0 && !utf8.RuneStart(s[end]) {
			end--
		}
		if end == 0 {
			return "" // all bytes are mid-rune continuations; return empty to guarantee valid UTF-8
		}
		return s[:end]
	}
	// Reserve 3 bytes for "..." so total output ≤ maxLen
	cutoff := maxLen - 3
	end := cutoff
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	if end == 0 {
		end = cutoff
	}
	return s[:end] + "..."
}

// TruncateTail shortens a string to at most maxLen bytes, keeping the END
// of the string (most recent content). Prepends "..." if truncation occurs.
// Returns s unchanged if maxLen <= 0 or s fits.
func TruncateTail(s string, maxLen int) string {
	if maxLen <= 0 || len(s) <= maxLen {
		return s
	}
	if maxLen < 4 {
		start := len(s) - maxLen
		for start < len(s) && !utf8.RuneStart(s[start]) {
			start++
		}
		if start >= len(s) {
			return ""
		}
		return s[start:]
	}
	// Reserve 3 bytes for "..."
	start := len(s) - (maxLen - 3)
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	if start >= len(s) {
		start = len(s) - (maxLen - 3)
	}
	return "..." + s[start:]
}

// CDATAEscape escapes CDATA end markers ("]]>") in s so the string
// can be safely embedded inside a <![CDATA[...]]> section.
func CDATAEscape(s string) string {
	return strings.ReplaceAll(s, "]]>", "]]]]><![CDATA[>")
}

// XMLEscapeAttr escapes characters that are unsafe in XML attribute values.
func XMLEscapeAttr(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}
