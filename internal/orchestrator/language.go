package orchestrator

import (
	"path/filepath"
	"strings"
)

// Language represents a programming language detected in a project.
type Language string

const (
	LanguageGo         Language = "go"
	LanguagePython     Language = "python"
	LanguageJavaScript Language = "javascript"
	LanguageTypeScript Language = "typescript"
	LanguageKotlin     Language = "kotlin"
	LanguageUnknown    Language = "unknown"
)

// languageExtensions maps file extensions to languages.
var languageExtensions = map[string]Language{
	".go":   LanguageGo,
	".py":   LanguagePython,
	".js":   LanguageJavaScript,
	".mjs":  LanguageJavaScript,
	".cjs":  LanguageJavaScript,
	".ts":   LanguageTypeScript,
	".tsx":  LanguageTypeScript,
	".kt":   LanguageKotlin,
	".kts":  LanguageKotlin,
}

// languageTeams maps languages to their review teams.
var languageTeams = map[Language][]string{
	LanguageGo:         {"go-dev-team", "go-sec-team"},
	LanguagePython:     {"python-dev-team", "python-sec-team"},
	LanguageJavaScript: {"fe-dev-team", "fe-sec-team"},
	LanguageTypeScript: {"fe-dev-team", "fe-sec-team"},
	LanguageKotlin:     {"mobile-review"},
}

// DetectLanguage detects the primary language from a file path.
func DetectLanguage(filePath string) Language {
	ext := strings.ToLower(filepath.Ext(filePath))
	if lang, ok := languageExtensions[ext]; ok {
		return lang
	}
	return LanguageUnknown
}

// DetectLanguages detects all languages from a list of file paths.
// Returns a map of language to count of files.
func DetectLanguages(filePaths []string) map[Language]int {
	counts := make(map[Language]int)
	for _, path := range filePaths {
		lang := DetectLanguage(path)
		if lang != LanguageUnknown {
			counts[lang]++
		}
	}
	return counts
}

// GetPrimaryLanguage returns the most common language from a list of files.
// Returns LanguageUnknown if no files or all unknown extensions.
func GetPrimaryLanguage(filePaths []string) Language {
	counts := DetectLanguages(filePaths)
	if len(counts) == 0 {
		return LanguageUnknown
	}

	var primary Language
	var maxCount int
	for lang, count := range counts {
		if count > maxCount {
			primary = lang
			maxCount = count
		}
	}
	return primary
}

// GetReviewTeams returns the appropriate review teams for a language.
// Returns empty slice if language is unknown.
func GetReviewTeams(lang Language) []string {
	teams, ok := languageTeams[lang]
	if !ok {
		return []string{}
	}
	return teams
}

// GetReviewTeamsForFiles returns review teams based on file paths.
// Uses the primary language detected from the files.
func GetReviewTeamsForFiles(filePaths []string) []string {
	primary := GetPrimaryLanguage(filePaths)
	return GetReviewTeams(primary)
}

// GetAllReviewTeamsForFiles returns all review teams for all languages detected.
// Useful when a project has multiple languages.
func GetAllReviewTeamsForFiles(filePaths []string) []string {
	counts := DetectLanguages(filePaths)

	teamSet := make(map[string]bool)
	for lang := range counts {
		teams := GetReviewTeams(lang)
		for _, team := range teams {
			teamSet[team] = true
		}
	}

	// Convert set to slice
	result := make([]string, 0, len(teamSet))
	for team := range teamSet {
		result = append(result, team)
	}
	return result
}
