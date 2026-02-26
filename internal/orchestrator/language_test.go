package orchestrator

import (
	"testing"
)

func TestDetectLanguage(t *testing.T) {
	tests := []struct {
		path string
		want Language
	}{
		{"main.go", LanguageGo},
		{"src/main.go", LanguageGo},
		{"app.py", LanguagePython},
		{"lib/utils.py", LanguagePython},
		{"app.js", LanguageJavaScript},
		{"module.mjs", LanguageJavaScript},
		{"config.cjs", LanguageJavaScript},
		{"app.ts", LanguageTypeScript},
		{"component.tsx", LanguageTypeScript},
		{"MainActivity.kt", LanguageKotlin},
		{"build.gradle.kts", LanguageKotlin},
		{"README.md", LanguageUnknown},
		{"Makefile", LanguageUnknown},
		{"", LanguageUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := DetectLanguage(tt.path)
			if got != tt.want {
				t.Errorf("DetectLanguage(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestDetectLanguages(t *testing.T) {
	files := []string{
		"main.go",
		"handler.go",
		"app.py",
		"utils.py",
		"test.py",
		"app.js",
		"README.md",
	}

	got := DetectLanguages(files)

	want := map[Language]int{
		LanguageGo:         2,
		LanguagePython:     3,
		LanguageJavaScript: 1,
	}

	if len(got) != len(want) {
		t.Errorf("DetectLanguages() returned %d languages, want %d", len(got), len(want))
	}

	for lang, count := range want {
		if got[lang] != count {
			t.Errorf("DetectLanguages() %v count = %d, want %d", lang, got[lang], count)
		}
	}
}

func TestGetPrimaryLanguage(t *testing.T) {
	tests := []struct {
		name  string
		files []string
		want  Language
	}{
		{
			name:  "mostly go",
			files: []string{"main.go", "handler.go", "utils.go", "app.py"},
			want:  LanguageGo,
		},
		{
			name:  "mostly python",
			files: []string{"app.py", "utils.py", "test.py", "main.go"},
			want:  LanguagePython,
		},
		{
			name:  "only javascript",
			files: []string{"app.js", "utils.js"},
			want:  LanguageJavaScript,
		},
		{
			name:  "no code files",
			files: []string{"README.md", "Makefile"},
			want:  LanguageUnknown,
		},
		{
			name:  "empty list",
			files: []string{},
			want:  LanguageUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetPrimaryLanguage(tt.files)
			if got != tt.want {
				t.Errorf("GetPrimaryLanguage() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetReviewTeams(t *testing.T) {
	tests := []struct {
		lang Language
		want []string
	}{
		{LanguageGo, []string{"go-dev-team", "go-sec-team"}},
		{LanguagePython, []string{"python-dev-team", "python-sec-team"}},
		{LanguageJavaScript, []string{"fe-dev-team", "fe-sec-team"}},
		{LanguageTypeScript, []string{"fe-dev-team", "fe-sec-team"}},
		{LanguageKotlin, []string{"mobile-review"}},
		{LanguageUnknown, []string{}},
	}

	for _, tt := range tests {
		t.Run(string(tt.lang), func(t *testing.T) {
			got := GetReviewTeams(tt.lang)
			if len(got) != len(tt.want) {
				t.Errorf("GetReviewTeams(%v) returned %d teams, want %d", tt.lang, len(got), len(tt.want))
				return
			}
			for i, team := range tt.want {
				if got[i] != team {
					t.Errorf("GetReviewTeams(%v)[%d] = %v, want %v", tt.lang, i, got[i], team)
				}
			}
		})
	}
}

func TestGetReviewTeamsForFiles(t *testing.T) {
	tests := []struct {
		name  string
		files []string
		want  []string
	}{
		{
			name:  "go project",
			files: []string{"main.go", "handler.go", "utils.go"},
			want:  []string{"go-dev-team", "go-sec-team"},
		},
		{
			name:  "python project",
			files: []string{"app.py", "utils.py", "test.py"},
			want:  []string{"python-dev-team", "python-sec-team"},
		},
		{
			name:  "mixed project (go primary)",
			files: []string{"main.go", "handler.go", "app.py"},
			want:  []string{"go-dev-team", "go-sec-team"},
		},
		{
			name:  "no code files",
			files: []string{"README.md", "LICENSE"},
			want:  []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetReviewTeamsForFiles(tt.files)
			if len(got) != len(tt.want) {
				t.Errorf("GetReviewTeamsForFiles() returned %d teams, want %d", len(got), len(tt.want))
				return
			}
			for i, team := range tt.want {
				if got[i] != team {
					t.Errorf("GetReviewTeamsForFiles()[%d] = %v, want %v", i, got[i], team)
				}
			}
		})
	}
}

func TestGetAllReviewTeamsForFiles(t *testing.T) {
	tests := []struct {
		name  string
		files []string
		want  map[string]bool // Using map for set comparison
	}{
		{
			name:  "single language",
			files: []string{"main.go", "handler.go"},
			want: map[string]bool{
				"go-dev-team": true,
				"go-sec-team": true,
			},
		},
		{
			name:  "multiple languages",
			files: []string{"main.go", "app.py", "utils.js"},
			want: map[string]bool{
				"go-dev-team":     true,
				"go-sec-team":     true,
				"python-dev-team": true,
				"python-sec-team": true,
				"fe-dev-team":     true,
				"fe-sec-team":     true,
			},
		},
		{
			name:  "no code files",
			files: []string{"README.md"},
			want:  map[string]bool{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetAllReviewTeamsForFiles(tt.files)

			if len(got) != len(tt.want) {
				t.Errorf("GetAllReviewTeamsForFiles() returned %d teams, want %d", len(got), len(tt.want))
				t.Errorf("Got: %v", got)
				t.Errorf("Want: %v", tt.want)
				return
			}

			// Check all expected teams are present
			gotSet := make(map[string]bool)
			for _, team := range got {
				gotSet[team] = true
			}

			for team := range tt.want {
				if !gotSet[team] {
					t.Errorf("GetAllReviewTeamsForFiles() missing team %v", team)
				}
			}
		})
	}
}
