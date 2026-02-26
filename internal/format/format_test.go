package format

import "testing"

func TestTokens(t *testing.T) {
	tests := []struct {
		input int
		want  string
	}{
		{0, "0"},
		{1, "1"},
		{999, "999"},
		{1000, "1.0k"},
		{1500, "1.5k"},
		{10000, "10.0k"},
		{999999, "1000.0k"},
		{1000000, "1.0M"},
		{1500000, "1.5M"},
		{10000000, "10.0M"},
	}
	for _, tt := range tests {
		got := Tokens(tt.input)
		if got != tt.want {
			t.Errorf("Tokens(%d) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{"empty", "", 10, ""},
		{"fits", "hello", 10, "hello"},
		{"exact", "hello", 5, "hello"},
		{"zero maxLen", "hello", 0, "hello"},
		{"negative maxLen", "hello", -1, "hello"},
		{"truncate ascii", "hello world", 8, "hello..."},
		{"truncate short", "hello world", 4, "h..."},
		{"very short maxLen", "hello", 3, "hel"},
		{"maxLen 1", "hello", 1, "h"},
		{"maxLen 2", "hello", 2, "he"},
		{"truncate with ellipsis budget", "abcdefghij", 7, "abcd..."},

		// UTF-8 tests
		{"utf8 fits", "héllo", 10, "héllo"},
		{"utf8 truncate", "héllo world", 8, "héll..."},
		// "é" is 2 bytes, so "héllo" is 6 bytes
		{"utf8 exact boundary", "héllo", 6, "héllo"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Truncate(tt.input, tt.maxLen)
			if got != tt.want {
				t.Errorf("Truncate(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
			}
			if tt.maxLen > 0 && len(got) > tt.maxLen {
				t.Errorf("Truncate(%q, %d) result length %d exceeds maxLen", tt.input, tt.maxLen, len(got))
			}
		})
	}
}

func TestCDATAEscape(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"no escape needed", "hello world", "hello world"},
		{"empty string", "", ""},
		{"single end marker", "foo ]]> bar", "foo ]]]]><![CDATA[> bar"},
		{"multiple end markers", "a ]]> b ]]> c", "a ]]]]><![CDATA[> b ]]]]><![CDATA[> c"},
		{"end marker at start", "]]>rest", "]]]]><![CDATA[>rest"},
		{"end marker at end", "start]]>", "start]]]]><![CDATA[>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CDATAEscape(tt.input)
			if got != tt.want {
				t.Errorf("CDATAEscape(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestXMLEscapeAttr(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"hello", "hello"},
		{`a & b`, "a &amp; b"},
		{`a "b" c`, "a &quot;b&quot; c"},
		{"a <b> c", "a &lt;b&gt; c"},
		{`a & "b" <c>`, "a &amp; &quot;b&quot; &lt;c&gt;"},
	}
	for _, tt := range tests {
		got := XMLEscapeAttr(tt.input)
		if got != tt.want {
			t.Errorf("XMLEscapeAttr(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
