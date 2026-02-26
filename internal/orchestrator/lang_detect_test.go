package orchestrator

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectProjectLanguages(t *testing.T) {
	// Create a temporary test directory
	tmpDir := t.TempDir()

	// Create test files
	testFiles := map[string]string{
		"main.go":           "package main",
		"app.py":            "# Python app",
		"script.py":         "# Python script",
		"index.js":          "// JavaScript",
		"component.tsx":     "// TypeScript React",
		"styles.css":        "/* CSS */",
		"MainActivity.kt":   "// Kotlin",
		".gitignore":        "node_modules",
		"__pycache__/x.pyc": "binary",
	}

	for path, content := range testFiles {
		fullPath := filepath.Join(tmpDir, path)
		dir := filepath.Dir(fullPath)
		if dir != tmpDir {
			os.MkdirAll(dir, 0755)
		}
		os.WriteFile(fullPath, []byte(content), 0644)
	}

	stats, err := DetectProjectLanguages(tmpDir)
	if err != nil {
		t.Fatalf("DetectProjectLanguages failed: %v", err)
	}

	// Verify counts
	if stats.Go != 1 {
		t.Errorf("Expected 1 Go file, got %d", stats.Go)
	}
	if stats.Python != 2 {
		t.Errorf("Expected 2 Python files, got %d", stats.Python)
	}
	if stats.JavaScript != 1 {
		t.Errorf("Expected 1 JavaScript file, got %d", stats.JavaScript)
	}
	if stats.TypeScript != 1 {
		t.Errorf("Expected 1 TypeScript file, got %d", stats.TypeScript)
	}
	if stats.CSS != 1 {
		t.Errorf("Expected 1 CSS file, got %d", stats.CSS)
	}
	if stats.Kotlin != 1 {
		t.Errorf("Expected 1 Kotlin file, got %d", stats.Kotlin)
	}
}

func TestPrimaryLanguage(t *testing.T) {
	tests := []struct {
		name     string
		stats    LanguageStats
		expected string
	}{
		{
			name:     "Go project",
			stats:    LanguageStats{Go: 50, Python: 5, JavaScript: 2},
			expected: "go",
		},
		{
			name:     "Python project",
			stats:    LanguageStats{Go: 0, Python: 30, JavaScript: 5},
			expected: "python",
		},
		{
			name:     "JavaScript project",
			stats:    LanguageStats{JavaScript: 40, TypeScript: 10, CSS: 5},
			expected: "javascript",
		},
		{
			name:     "TypeScript project",
			stats:    LanguageStats{TypeScript: 50, JavaScript: 10},
			expected: "typescript",
		},
		{
			name:     "Kotlin project",
			stats:    LanguageStats{Kotlin: 25},
			expected: "kotlin",
		},
		{
			name:     "Empty project",
			stats:    LanguageStats{},
			expected: "unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.stats.PrimaryLanguage()
			if result != tt.expected {
				t.Errorf("Expected %s, got %s", tt.expected, result)
			}
		})
	}
}

func TestAllLanguages(t *testing.T) {
	stats := LanguageStats{
		Go:         10,
		Python:     5,
		JavaScript: 3,
		TypeScript: 0,
		Kotlin:     0,
		CSS:        2,
	}

	langs := stats.AllLanguages()

	expected := []string{"go", "python", "javascript", "css"}
	if len(langs) != len(expected) {
		t.Errorf("Expected %d languages, got %d", len(expected), len(langs))
	}

	// Check all expected languages are present
	langMap := make(map[string]bool)
	for _, lang := range langs {
		langMap[lang] = true
	}

	for _, exp := range expected {
		if !langMap[exp] {
			t.Errorf("Expected language %s not found in results", exp)
		}
	}
}

func TestGetReviewTeamForLanguage(t *testing.T) {
	tests := []struct {
		name           string
		language       string
		securityFocus  bool
		expectedTeam   string
	}{
		// Development teams
		{"Go dev", "go", false, "go-dev-team"},
		{"Python dev", "python", false, "python-dev-team"},
		{"JavaScript dev", "javascript", false, "fe-dev-team"},
		{"TypeScript dev", "typescript", false, "fe-dev-team"},
		{"Kotlin dev", "kotlin", false, "mobile-review"},
		{"Unknown dev", "rust", false, "go-dev-team"},

		// Security teams
		{"Go sec", "go", true, "go-sec-team"},
		{"Python sec", "python", true, "python-sec-team"},
		{"JavaScript sec", "javascript", true, "fe-sec-team"},
		{"TypeScript sec", "typescript", true, "fe-sec-team"},
		{"Kotlin sec", "kotlin", true, "mobile-review"},
		{"Unknown sec", "rust", true, "go-sec-team"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetReviewTeamForLanguage(tt.language, tt.securityFocus)
			if result != tt.expectedTeam {
				t.Errorf("Expected %s, got %s", tt.expectedTeam, result)
			}
		})
	}
}

func TestDetectAndSelectTeam(t *testing.T) {
	// Create a Python project
	tmpDir := t.TempDir()

	pythonFiles := []string{
		"app.py",
		"models.py",
		"views.py",
		"tests/test_app.py",
	}

	for _, file := range pythonFiles {
		fullPath := filepath.Join(tmpDir, file)
		dir := filepath.Dir(fullPath)
		if dir != tmpDir {
			os.MkdirAll(dir, 0755)
		}
		os.WriteFile(fullPath, []byte("# Python"), 0644)
	}

	// Test dev team selection
	team, err := DetectAndSelectTeam(tmpDir, false)
	if err != nil {
		t.Fatalf("DetectAndSelectTeam failed: %v", err)
	}
	if team != "python-dev-team" {
		t.Errorf("Expected python-dev-team, got %s", team)
	}

	// Test security team selection
	secTeam, err := DetectAndSelectTeam(tmpDir, true)
	if err != nil {
		t.Fatalf("DetectAndSelectTeam failed: %v", err)
	}
	if secTeam != "python-sec-team" {
		t.Errorf("Expected python-sec-team, got %s", secTeam)
	}
}
