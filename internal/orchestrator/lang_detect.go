package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
)

// LanguageStats holds file counts for each detected language
type LanguageStats struct {
	Go         int
	Python     int
	JavaScript int
	TypeScript int
	Kotlin     int
	CSS        int
	Other      int
}

// DetectProjectLanguages scans a directory and returns statistics about
// which languages are present based on file extensions.
func DetectProjectLanguages(projectDir string) (*LanguageStats, error) {
	stats := &LanguageStats{}

	err := filepath.Walk(projectDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip errors, continue walking
		}

		if info.IsDir() {
			// Skip common non-source directories
			name := info.Name()
			if name == ".git" || name == "node_modules" || name == "__pycache__" ||
				name == "vendor" || name == ".venv" || name == "venv" ||
				name == "dist" || name == "build" {
				return filepath.SkipDir
			}
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))
		switch ext {
		case ".go":
			stats.Go++
		case ".py":
			stats.Python++
		case ".js", ".mjs", ".cjs":
			stats.JavaScript++
		case ".ts", ".tsx":
			stats.TypeScript++
		case ".kt", ".kts":
			stats.Kotlin++
		case ".css", ".scss", ".sass":
			stats.CSS++
		default:
			if ext != "" {
				stats.Other++
			}
		}
		return nil
	})

	return stats, err
}

// PrimaryLanguage returns the dominant language based on file counts
func (s *LanguageStats) PrimaryLanguage() string {
	max := 0
	primary := "unknown"

	if s.Go > max {
		max = s.Go
		primary = "go"
	}
	if s.Python > max {
		max = s.Python
		primary = "python"
	}
	if s.JavaScript > max {
		max = s.JavaScript
		primary = "javascript"
	}
	if s.TypeScript > max {
		max = s.TypeScript
		primary = "typescript"
	}
	if s.Kotlin > max {
		max = s.Kotlin
		primary = "kotlin"
	}

	return primary
}

// AllLanguages returns a slice of all languages with at least one file
func (s *LanguageStats) AllLanguages() []string {
	var langs []string
	if s.Go > 0 {
		langs = append(langs, "go")
	}
	if s.Python > 0 {
		langs = append(langs, "python")
	}
	if s.JavaScript > 0 {
		langs = append(langs, "javascript")
	}
	if s.TypeScript > 0 {
		langs = append(langs, "typescript")
	}
	if s.Kotlin > 0 {
		langs = append(langs, "kotlin")
	}
	if s.CSS > 0 {
		langs = append(langs, "css")
	}
	return langs
}

// GetReviewTeamForLanguage returns the appropriate review team name
// based on the detected language and whether security focus is needed
func GetReviewTeamForLanguage(language string, securityFocus bool) string {
	if securityFocus {
		switch language {
		case "go":
			return "go-sec-team"
		case "python":
			return "python-sec-team"
		case "javascript", "typescript":
			return "fe-sec-team"
		case "kotlin":
			return "mobile-review" // Includes security
		default:
			return "go-sec-team" // fallback
		}
	}

	// Development teams
	switch language {
	case "go":
		return "go-dev-team"
	case "python":
		return "python-dev-team"
	case "javascript", "typescript":
		return "fe-dev-team"
	case "kotlin":
		return "mobile-review"
	default:
		return "go-dev-team" // fallback
	}
}

// DetectAndSelectTeam is a convenience function that detects the primary
// language in a directory and returns the appropriate review team name
func DetectAndSelectTeam(projectDir string, securityFocus bool) (string, error) {
	stats, err := DetectProjectLanguages(projectDir)
	if err != nil {
		return "", err
	}

	primary := stats.PrimaryLanguage()
	return GetReviewTeamForLanguage(primary, securityFocus), nil
}
