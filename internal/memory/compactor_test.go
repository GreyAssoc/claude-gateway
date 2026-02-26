package memory

import (
	"strings"
	"testing"
)

func TestBuildSimpleSummary(t *testing.T) {
	tests := []struct {
		name         string
		count        int
		inputTokens  int
		outputTokens int
		topics       []string
		wantContains []string
	}{
		{
			name:         "no topics",
			count:        5,
			inputTokens:  1234,
			outputTokens: 567,
			topics:       nil,
			wantContains: []string{"5 interactions", "1.2k input tokens", "567 output tokens"},
		},
		{
			name:         "with topics",
			count:        10,
			inputTokens:  50000,
			outputTokens: 12000,
			topics:       []string{"deploy server", "fix bug", "review code"},
			wantContains: []string{"10 interactions", "50.0k input tokens", "12.0k output tokens", "Topics:", "deploy server", "fix bug", "review code"},
		},
		{
			name:         "topics capped at 5",
			count:        1,
			inputTokens:  100,
			outputTokens: 50,
			topics:       []string{"a", "b", "c", "d", "e", "f", "g"},
			wantContains: []string{"1 interactions", "Topics:", "e"},
		},
		{
			name:         "zero counts",
			count:        0,
			inputTokens:  0,
			outputTokens: 0,
			topics:       nil,
			wantContains: []string{"0 interactions", "0 input tokens", "0 output tokens"},
		},
		{
			name:         "million token scale",
			count:        100,
			inputTokens:  2500000,
			outputTokens: 1000000,
			topics:       nil,
			wantContains: []string{"100 interactions", "2.5M input tokens", "1.0M output tokens"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildSimpleSummary(tt.count, tt.inputTokens, tt.outputTokens, tt.topics)
			for _, want := range tt.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("buildSimpleSummary() = %q, want it to contain %q", got, want)
				}
			}
		})
	}
}

func TestBuildSimpleSummary_TopicsNotIncludeSixth(t *testing.T) {
	topics := []string{"a", "b", "c", "d", "e", "f"}
	got := buildSimpleSummary(1, 100, 50, topics)
	if strings.Contains(got, "; f") {
		t.Errorf("buildSimpleSummary should only include first 5 topics, but got %q", got)
	}
}
