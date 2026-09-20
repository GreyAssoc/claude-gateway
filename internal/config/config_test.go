package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveInt(t *testing.T) {
	tests := []struct {
		name     string
		val      any
		fallback int
		want     int
	}{
		{"int value", 42, 0, 42},
		{"int64 value", int64(42), 0, 42},
		{"float64 value", 42.0, 0, 42},
		{"string number", "42", 0, 42},
		{"nil", nil, 99, 99},
		{"invalid string", "abc", 99, 99},
		{"empty string", "", 99, 99},
		{"bool", true, 99, 99},
		{"float64 overflow", float64(1e30), 99, 99},
		{"float64 negative overflow", float64(-1e30), 99, 99},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveInt(tt.val, tt.fallback)
			if got != tt.want {
				t.Errorf("resolveInt(%v, %d) = %d, want %d", tt.val, tt.fallback, got, tt.want)
			}
		})
	}
}

func TestLoad_DefaultValues(t *testing.T) {
	// Set minimum required env vars
	t.Setenv("TELEGRAM_BOT_TOKEN", "test-token")
	t.Setenv("ALLOWED_USER_IDS", "123")
	t.Setenv("CRON_FRAMEWORK", "")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// Check defaults
	if cfg.Sidecar.TimeoutSeconds != 7200 {
		t.Errorf("Sidecar.TimeoutSeconds = %d, want 7200", cfg.Sidecar.TimeoutSeconds)
	}
	if cfg.Heartbeat.IntervalSeconds != 300 {
		t.Errorf("Heartbeat.IntervalSeconds = %d, want 300", cfg.Heartbeat.IntervalSeconds)
	}
	if cfg.Heartbeat.MaxFailures != 3 {
		t.Errorf("Heartbeat.MaxFailures = %d, want 3", cfg.Heartbeat.MaxFailures)
	}
	if cfg.RateLimiter.SafetyMargin != 0.85 {
		t.Errorf("RateLimiter.SafetyMargin = %f, want 0.85", cfg.RateLimiter.SafetyMargin)
	}
	if cfg.Database.Path != "data/gateway.db" {
		t.Errorf("Database.Path = %q, want data/gateway.db", cfg.Database.Path)
	}
	if cfg.Orchestrator.Timeout != 600 {
		t.Errorf("Orchestrator.Timeout = %d, want 600", cfg.Orchestrator.Timeout)
	}
	if cfg.Dashboard.Port != 8080 {
		t.Errorf("Dashboard.Port = %d, want 8080", cfg.Dashboard.Port)
	}
	if cfg.Sidecar.DefaultModel != "sonnet" {
		t.Errorf("Sidecar.DefaultModel = %q, want sonnet", cfg.Sidecar.DefaultModel)
	}
	if len(cfg.Skills.Dirs) < 2 || cfg.Skills.Dirs[0] != "harness/skills" {
		t.Errorf("Skills.Dirs = %#v, want harness/skills first", cfg.Skills.Dirs)
	}
	if cfg.CronFramework != "qwen" {
		t.Errorf("CronFramework = %q, want qwen", cfg.CronFramework)
	}
}

func TestLoad_CronFrameworkEnv(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "test-token")
	t.Setenv("ALLOWED_USER_IDS", "123")
	t.Setenv("CRON_FRAMEWORK", "grok")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.CronFramework != "grok" {
		t.Errorf("CronFramework = %q, want grok from env", cfg.CronFramework)
	}
}

func TestNormalizeSkillsDirs(t *testing.T) {
	got := normalizeSkillsDirs(nil)
	if len(got) != 2 || got[0] != "harness/skills" || got[1] != "skills" {
		t.Errorf("empty = %#v", got)
	}
	got = normalizeSkillsDirs([]string{"skills"})
	if len(got) != 2 || got[0] != "harness/skills" {
		t.Errorf("prepend = %#v", got)
	}
	got = normalizeSkillsDirs([]string{"harness/skills", "skills"})
	if len(got) != 2 {
		t.Errorf("already present duplicated: %#v", got)
	}
}

func TestLoad_MissingToken(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	t.Setenv("ALLOWED_USER_IDS", "123")

	_, err := Load("")
	if err == nil {
		t.Error("Expected error for missing bot token")
	}
}

func TestLoad_MissingUserIDs(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "test-token")
	t.Setenv("ALLOWED_USER_IDS", "")

	_, err := Load("")
	if err == nil {
		t.Error("Expected error for missing user IDs")
	}
}

func TestLoad_MultipleUserIDs(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "test-token")
	t.Setenv("ALLOWED_USER_IDS", "123, 456, 789")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if len(cfg.Telegram.AllowedUserIDs) != 3 {
		t.Errorf("AllowedUserIDs count = %d, want 3", len(cfg.Telegram.AllowedUserIDs))
	}
	for _, id := range []int64{123, 456, 789} {
		if !cfg.Telegram.AllowedUserIDs[id] {
			t.Errorf("AllowedUserIDs missing %d", id)
		}
	}
}

func TestLoad_NegativeMemoryConfigDefaulted(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "test-token")
	t.Setenv("ALLOWED_USER_IDS", "123")

	yamlContent := `
memory:
  max_curated_inject: -5
  max_daily_days: -3
`
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "test.yaml")
	if err := os.WriteFile(yamlPath, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("writing temp yaml: %v", err)
	}

	cfg, err := Load(yamlPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg.Memory.MaxCuratedInject != 20 {
		t.Errorf("Memory.MaxCuratedInject = %d after negative YAML, want 20", cfg.Memory.MaxCuratedInject)
	}
	if cfg.Memory.MaxDailyDays != 7 {
		t.Errorf("Memory.MaxDailyDays = %d after negative YAML, want 7", cfg.Memory.MaxDailyDays)
	}
}

func TestLoad_NegativeTimeoutsDefaulted(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "test-token")
	t.Setenv("ALLOWED_USER_IDS", "123")

	// Write a temp YAML file with negative values to verify Load() applies defaults.
	yamlContent := `
sidecar:
  timeout_seconds: -1
heartbeat:
  interval_seconds: -10
  max_failures: -1
orchestrator:
  timeout_seconds: -5
`
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "test.yaml")
	if err := os.WriteFile(yamlPath, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("writing temp yaml: %v", err)
	}

	cfg, err := Load(yamlPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg.Sidecar.TimeoutSeconds != 7200 {
		t.Errorf("Sidecar.TimeoutSeconds = %d after negative YAML, want 7200", cfg.Sidecar.TimeoutSeconds)
	}
	if cfg.Orchestrator.Timeout != 600 {
		t.Errorf("Orchestrator.Timeout = %d after negative YAML, want 600", cfg.Orchestrator.Timeout)
	}
	if cfg.Heartbeat.IntervalSeconds != 300 {
		t.Errorf("Heartbeat.IntervalSeconds = %d after negative YAML, want 300", cfg.Heartbeat.IntervalSeconds)
	}
	if cfg.Heartbeat.MaxFailures != 3 {
		t.Errorf("Heartbeat.MaxFailures = %d after negative YAML, want 3", cfg.Heartbeat.MaxFailures)
	}
}

func TestLoad_DefaultModelFromYAML(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "test-token")
	t.Setenv("ALLOWED_USER_IDS", "123")

	yamlContent := `
sidecar:
  default_model: "haiku"
`
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "test.yaml")
	if err := os.WriteFile(yamlPath, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("writing temp yaml: %v", err)
	}

	cfg, err := Load(yamlPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg.Sidecar.DefaultModel != "haiku" {
		t.Errorf("Sidecar.DefaultModel = %q, want haiku", cfg.Sidecar.DefaultModel)
	}
}

func TestLoad_NonexistentYAML(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "test-token")
	t.Setenv("ALLOWED_USER_IDS", "123")

	_, err := Load("/nonexistent/path/config.yaml")
	if err == nil {
		t.Fatal("Expected error for nonexistent YAML path")
	}
	// Error should include the file path for debugging
	if !strings.Contains(err.Error(), "/nonexistent/path/config.yaml") {
		t.Errorf("Error should include file path, got: %v", err)
	}
}

func TestLoad_InvalidYAML(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "test-token")
	t.Setenv("ALLOWED_USER_IDS", "123")

	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(yamlPath, []byte("key: [unclosed bracket"), 0644); err != nil {
		t.Fatalf("writing temp yaml: %v", err)
	}

	_, err := Load(yamlPath)
	if err == nil {
		t.Fatal("Expected error for invalid YAML")
	}
	// Error should include the file path for debugging
	if !strings.Contains(err.Error(), yamlPath) {
		t.Errorf("Error should include file path, got: %v", err)
	}
}

func TestLoad_MinSimilarityUpperBound(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "test-token")
	t.Setenv("ALLOWED_USER_IDS", "123")

	yamlContent := `
context:
  min_similarity: 1.5
`
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "test.yaml")
	if err := os.WriteFile(yamlPath, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("writing temp yaml: %v", err)
	}

	cfg, err := Load(yamlPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg.Context.MinSimilarity != 0.75 {
		t.Errorf("Context.MinSimilarity = %f after > 1.0 YAML value, want 0.75", cfg.Context.MinSimilarity)
	}
}
