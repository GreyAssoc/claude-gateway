package profile

import (
	"encoding/json"
	"testing"
)

// TestFormatAspectName verifies snake_case to Title Case conversion.
func TestFormatAspectName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"query_style", "Query Style"},
		{"communication_style", "Communication Style"},
		{"priorities", "Priorities"},
		{"tools_preferences", "Tools Preferences"},
		{"", ""},
		{"single", "Single"},
		{"a_b_c", "A B C"},
	}
	for _, tt := range tests {
		got := formatAspectName(tt.input)
		if got != tt.want {
			t.Errorf("formatAspectName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// TestExplicitEvidenceDetection verifies that evidence with source="explicit"
// is correctly detected when unmarshaled from JSON. This is the core logic
// that prevents auto-observations from degrading user-set profile aspects.
func TestExplicitEvidenceDetection(t *testing.T) {
	tests := []struct {
		name        string
		evidence    []Evidence
		wantPinned  bool
	}{
		{
			name:       "explicit source detected",
			evidence:   []Evidence{{Observation: "User set", Date: "2024-01-01", Source: "explicit"}},
			wantPinned: true,
		},
		{
			name:       "conversation source not pinned",
			evidence:   []Evidence{{Observation: "auto obs", Date: "2024-01-01", Source: "conversation"}},
			wantPinned: false,
		},
		{
			name:       "journal source not pinned",
			evidence:   []Evidence{{Observation: "journal obs", Date: "2024-01-01", Source: "journal"}},
			wantPinned: false,
		},
		{
			name: "mixed — explicit among others",
			evidence: []Evidence{
				{Observation: "auto obs", Date: "2024-01-01", Source: "conversation"},
				{Observation: "User set", Date: "2024-01-02", Source: "explicit"},
				{Observation: "another auto", Date: "2024-01-03", Source: "conversation"},
			},
			wantPinned: true,
		},
		{
			name:       "empty evidence",
			evidence:   nil,
			wantPinned: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evJSON, err := json.Marshal(tt.evidence)
			if err != nil {
				t.Fatalf("marshal evidence: %v", err)
			}

			var parsed []Evidence
			if err := json.Unmarshal(evJSON, &parsed); err != nil {
				// nil evidence marshals as "null", which unmarshals to nil slice
				parsed = nil
			}

			gotPinned := false
			for _, ev := range parsed {
				if ev.Source == "explicit" {
					gotPinned = true
					break
				}
			}

			if gotPinned != tt.wantPinned {
				t.Errorf("explicit detection = %v, want %v", gotPinned, tt.wantPinned)
			}
		})
	}
}

// TestNewProfilerNilDBPanics verifies that NewProfiler panics with nil db,
// matching the established pattern in peer constructors (NewBuilder, NewManager).
func TestNewProfilerNilDBPanics(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic with nil db, got none")
		}
		msg, ok := r.(string)
		if !ok || msg != "profile: nil db" {
			t.Errorf("unexpected panic value: %v", r)
		}
	}()
	NewProfiler(nil, nil)
}
