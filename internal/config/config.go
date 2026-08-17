// Package config loads and validates gateway configuration from YAML and environment variables.
package config

import (
	"fmt"
	"log"
	"math"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Framework       string // "claude" or "opencode" (default: "claude")
	Telegram        Telegram
	Sidecar         Sidecar
	OpenCode        OpenCode
	RateLimiter     RateLimiter
	Cron            map[string]CronJob
	Watchers        WatchersConfig
	Heartbeat       Heartbeat
	Database        Database
	Skills          Skills
	Memory          Memory
	Orchestrator    Orchestrator
	Dashboard       Dashboard
	WorkDir         string
	PostgresURL     string
	OllamaURL       string
	OllamaEmbedModel string
	OllamaLLMModel  string
	Context         ContextConfig
	Triage          TriageConfig
	AgentSync       AgentSync
}

// OpenCode holds configuration for the OpenCode backend.
type OpenCode struct {
	Binary         string `yaml:"binary"`
	Model          string `yaml:"model"`
	TimeoutSeconds int    `yaml:"timeout_seconds"`
	AgentDir       string `yaml:"agent_dir"`
}

// AgentSync holds configuration for bidirectional agent/command syncing
// between Claude Code and OpenCode framework directories.
type AgentSync struct {
	Enabled          bool   `yaml:"enabled"`
	ClaudeAgentDir   string `yaml:"claude_agent_dir"`
	ClaudeCmdDir     string `yaml:"claude_cmd_dir"`
	OpencodeAgentDir string `yaml:"opencode_agent_dir"`
	OpencodeCmdDir   string `yaml:"opencode_cmd_dir"`
}

type ContextConfig struct {
	RecentTurns    int     `yaml:"recent_turns"`
	ReplayMessages int     `yaml:"replay_messages"`
	SemanticHits   int     `yaml:"semantic_hits"`
	MinSimilarity  float64 `yaml:"min_similarity"`
}

type TriageConfig struct {
	Enabled       bool `yaml:"enabled"`
	AutoLearn     bool `yaml:"auto_learn"`
	MaxAutoFacts  int  `yaml:"max_auto_facts"`
	MinImportance int  `yaml:"min_importance"`
}

type Telegram struct {
	BotToken       string
	AllowedUserIDs map[int64]bool
	NotifyChatID   int64
}

type Sidecar struct {
	URL            string
	TimeoutSeconds int
	HealthPath     string
	DefaultModel   string
}

type RateLimiter struct {
	RPM          int
	ITPM         int
	OTPM         int
	SafetyMargin float64
}

type CronJob struct {
	Schedule string `yaml:"schedule"`
	Prompt   string `yaml:"prompt"`
	Enabled  bool   `yaml:"enabled"`
}

type WatchersConfig struct {
	Gmail    WatcherConfig `yaml:"gmail"`
	Calendar WatcherConfig `yaml:"calendar"`
}

type WatcherConfig struct {
	Enabled         bool   `yaml:"enabled"`
	IntervalSeconds int    `yaml:"interval_seconds"`
	Prompt          string `yaml:"prompt"`
}

type Heartbeat struct {
	Enabled         bool `yaml:"enabled"`
	IntervalSeconds int  `yaml:"interval_seconds"`
	MaxFailures     int  `yaml:"max_failures"`
}

type Database struct {
	Path string `yaml:"path"`
}

type Skills struct {
	Dirs []string `yaml:"dirs"`
}

type Memory struct {
	BootstrapDir     string   `yaml:"bootstrap_dir"`
	ProfilesDir      string   `yaml:"profiles_dir"`
	DailyDir         string   `yaml:"daily_dir"`
	MaxCuratedInject int      `yaml:"max_curated_inject"`
	MaxDailyDays     int      `yaml:"max_daily_days"`
	ClaudeMDPaths    []string `yaml:"claude_md_paths"`
}

type Orchestrator struct {
	AgentDNADir string `yaml:"agent_dna_dir"`
	Timeout     int    `yaml:"timeout_seconds"`
}

type Dashboard struct {
	Enabled  bool   `yaml:"enabled"`
	Port     int    `yaml:"port"`
	BearerToken string `yaml:"-"`
}

// rawConfig mirrors the YAML structure before env var resolution
type rawConfig struct {
	Framework string             `yaml:"framework"`
	Cron      map[string]CronJob `yaml:"cron"`
	Watchers  WatchersConfig     `yaml:"watchers"`
	Heartbeat Heartbeat    `yaml:"heartbeat"`
	OpenCode  OpenCode     `yaml:"opencode"`
	Sidecar   struct {
		URL            string `yaml:"url"`
		TimeoutSeconds int    `yaml:"timeout_seconds"`
		HealthPath     string `yaml:"health_path"`
		DefaultModel   string `yaml:"default_model"`
	} `yaml:"sidecar"`
	RateLimiter struct {
		RPM          any `yaml:"rpm"`
		ITPM         any `yaml:"itpm"`
		OTPM         any `yaml:"otpm"`
		SafetyMargin float64     `yaml:"safety_margin"`
	} `yaml:"ratelimiter"`
	Database        Database      `yaml:"database"`
	Skills          Skills        `yaml:"skills"`
	Memory          Memory        `yaml:"memory"`
	Orchestrator    Orchestrator  `yaml:"orchestrator"`
	Dashboard       Dashboard     `yaml:"dashboard"`
	WorkDir         string        `yaml:"work_dir"`
	PostgresURL     string        `yaml:"postgres_url"`
	OllamaURL       string        `yaml:"ollama_url"`
	OllamaEmbedModel string       `yaml:"ollama_embed_model"`
	OllamaLLMModel  string        `yaml:"ollama_llm_model"`
	Context         ContextConfig `yaml:"context"`
	Triage          TriageConfig  `yaml:"triage"`
	AgentSync       AgentSync     `yaml:"agent_sync"`
}

func Load(yamlPath string) (*Config, error) {
	cfg := &Config{}

	// Load YAML if it exists
	if yamlPath != "" {
		if err := loadYAML(yamlPath, cfg); err != nil {
			return nil, fmt.Errorf("loading yaml: %w", err)
		}
	}

	// Override with env vars (env always wins)
	loadEnv(cfg)

	// Validate
	if cfg.Telegram.BotToken == "" {
		return nil, fmt.Errorf("TELEGRAM_BOT_TOKEN is required")
	}
	if len(cfg.Telegram.AllowedUserIDs) == 0 {
		return nil, fmt.Errorf("ALLOWED_USER_IDS is required")
	}

	// Validate rate limits are positive
	if cfg.RateLimiter.RPM <= 0 {
		cfg.RateLimiter.RPM = 50
	}
	if cfg.RateLimiter.ITPM <= 0 {
		cfg.RateLimiter.ITPM = 40000
	}
	if cfg.RateLimiter.OTPM <= 0 {
		cfg.RateLimiter.OTPM = 8000
	}

	// Framework defaults
	if cfg.Framework == "" {
		cfg.Framework = "claude"
	}

	// OpenCode defaults
	if cfg.OpenCode.Binary == "" {
		cfg.OpenCode.Binary = "opencode"
	}
	if cfg.OpenCode.TimeoutSeconds <= 0 {
		cfg.OpenCode.TimeoutSeconds = 600
	}

	// Defaults
	if cfg.Sidecar.URL == "" {
		cfg.Sidecar.URL = "http://sidecar:3001"
	}
	if cfg.Sidecar.TimeoutSeconds <= 0 {
		cfg.Sidecar.TimeoutSeconds = 7200
	}
	if cfg.Sidecar.HealthPath == "" {
		cfg.Sidecar.HealthPath = "/health"
	}
	if cfg.Sidecar.DefaultModel == "" {
		cfg.Sidecar.DefaultModel = "sonnet"
	}
	if cfg.WorkDir == "" {
		cfg.WorkDir = "/home/user/project"
	}
	if cfg.RateLimiter.SafetyMargin <= 0 || cfg.RateLimiter.SafetyMargin > 1 {
		cfg.RateLimiter.SafetyMargin = 0.85
	}
	if cfg.Heartbeat.IntervalSeconds <= 0 {
		cfg.Heartbeat.IntervalSeconds = 300
	}
	if cfg.Heartbeat.MaxFailures <= 0 {
		cfg.Heartbeat.MaxFailures = 3
	}
	if cfg.Database.Path == "" {
		cfg.Database.Path = "data/gateway.db"
	}
	if len(cfg.Skills.Dirs) == 0 {
		cfg.Skills.Dirs = []string{"skills"}
	}
	if cfg.Memory.BootstrapDir == "" {
		cfg.Memory.BootstrapDir = "memory/bootstrap"
	}
	if cfg.Memory.ProfilesDir == "" {
		cfg.Memory.ProfilesDir = "memory/profiles"
	}
	if cfg.Memory.DailyDir == "" {
		cfg.Memory.DailyDir = "memory/daily"
	}
	if cfg.Memory.MaxCuratedInject <= 0 {
		cfg.Memory.MaxCuratedInject = 20
	}
	if cfg.Memory.MaxDailyDays <= 0 {
		cfg.Memory.MaxDailyDays = 7
	}
	if cfg.Orchestrator.AgentDNADir == "" {
		cfg.Orchestrator.AgentDNADir = "/app/agents"
	}
	if cfg.Orchestrator.Timeout <= 0 {
		cfg.Orchestrator.Timeout = 600
	}
	if cfg.Dashboard.Port == 0 {
		cfg.Dashboard.Port = 8080
	}

	// Ollama defaults
	if cfg.OllamaURL == "" {
		cfg.OllamaURL = "http://ollama:11434"
	}
	if cfg.OllamaEmbedModel == "" {
		cfg.OllamaEmbedModel = "nomic-embed-text"
	}
	if cfg.OllamaLLMModel == "" {
		cfg.OllamaLLMModel = "qwen2.5-coder:7b"
	}

	// Context defaults
	if cfg.Context.RecentTurns <= 0 {
		cfg.Context.RecentTurns = 20
	}
	if cfg.Context.ReplayMessages <= 0 {
		cfg.Context.ReplayMessages = 30
	}
	if cfg.Context.SemanticHits <= 0 {
		cfg.Context.SemanticHits = 5
	}
	if cfg.Context.MinSimilarity <= 0 || cfg.Context.MinSimilarity > 1.0 {
		cfg.Context.MinSimilarity = 0.75
	}

	// Triage defaults
	if cfg.Triage.MaxAutoFacts <= 0 {
		cfg.Triage.MaxAutoFacts = 200
	}
	if cfg.Triage.MinImportance <= 0 {
		cfg.Triage.MinImportance = 4
	}

	// AgentSync defaults — directories relative to WorkDir parent (.claude/ and .opencode/ at workspace root)
	if cfg.AgentSync.ClaudeAgentDir == "" {
		cfg.AgentSync.ClaudeAgentDir = cfg.WorkDir + "/.claude/agents"
	}
	if cfg.AgentSync.ClaudeCmdDir == "" {
		cfg.AgentSync.ClaudeCmdDir = cfg.WorkDir + "/.claude/commands"
	}
	if cfg.AgentSync.OpencodeAgentDir == "" {
		cfg.AgentSync.OpencodeAgentDir = cfg.WorkDir + "/.opencode/agents"
	}
	if cfg.AgentSync.OpencodeCmdDir == "" {
		cfg.AgentSync.OpencodeCmdDir = cfg.WorkDir + "/.opencode/commands"
	}

	return cfg, nil
}

func loadYAML(path string, cfg *Config) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading config file %s: %w", path, err)
	}

	var raw rawConfig
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("parsing config file %s: %w", path, err)
	}

	cfg.Framework = raw.Framework
	cfg.OpenCode = raw.OpenCode
	cfg.Cron = raw.Cron
	cfg.Watchers = raw.Watchers
	cfg.Heartbeat = raw.Heartbeat

	cfg.Sidecar.URL = raw.Sidecar.URL
	cfg.Sidecar.TimeoutSeconds = raw.Sidecar.TimeoutSeconds
	cfg.Sidecar.HealthPath = raw.Sidecar.HealthPath
	cfg.Sidecar.DefaultModel = raw.Sidecar.DefaultModel

	cfg.RateLimiter.SafetyMargin = raw.RateLimiter.SafetyMargin

	// Handle rate limiter values that might be env var references in YAML
	cfg.RateLimiter.RPM = resolveInt(raw.RateLimiter.RPM, 50)
	cfg.RateLimiter.ITPM = resolveInt(raw.RateLimiter.ITPM, 40000)
	cfg.RateLimiter.OTPM = resolveInt(raw.RateLimiter.OTPM, 8000)

	cfg.Database = raw.Database
	cfg.Skills = raw.Skills
	cfg.Memory = raw.Memory
	cfg.Orchestrator = raw.Orchestrator
	cfg.Dashboard = raw.Dashboard
	cfg.WorkDir = raw.WorkDir
	cfg.PostgresURL = raw.PostgresURL
	cfg.OllamaURL = raw.OllamaURL
	cfg.OllamaEmbedModel = raw.OllamaEmbedModel
	cfg.OllamaLLMModel = raw.OllamaLLMModel
	cfg.Context = raw.Context
	cfg.Triage = raw.Triage
	cfg.AgentSync = raw.AgentSync

	return nil
}

func loadEnv(cfg *Config) {
	if v := os.Getenv("TELEGRAM_BOT_TOKEN"); v != "" {
		cfg.Telegram.BotToken = v
	}

	if v := os.Getenv("ALLOWED_USER_IDS"); v != "" {
		cfg.Telegram.AllowedUserIDs = make(map[int64]bool)
		for _, idStr := range strings.Split(v, ",") {
			idStr = strings.TrimSpace(idStr)
			if idStr == "" {
				continue
			}
			if id, err := strconv.ParseInt(idStr, 10, 64); err == nil {
				cfg.Telegram.AllowedUserIDs[id] = true
			} else {
				log.Printf("WARNING: ignoring invalid user ID %q: %v", idStr, err)
			}
		}
	}

	if v := os.Getenv("TELEGRAM_CHAT_ID"); v != "" {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			cfg.Telegram.NotifyChatID = id
		} else {
			log.Printf("WARNING: ignoring invalid TELEGRAM_CHAT_ID %q: %v", v, err)
		}
	}

	if v := os.Getenv("FRAMEWORK"); v != "" {
		cfg.Framework = v
	}
	if v := os.Getenv("OPENCODE_BINARY"); v != "" {
		cfg.OpenCode.Binary = v
	}
	if v := os.Getenv("OPENCODE_MODEL"); v != "" {
		cfg.OpenCode.Model = v
	}
	if v := os.Getenv("SIDECAR_URL"); v != "" {
		cfg.Sidecar.URL = v
	}
	if v := os.Getenv("WORK_DIR"); v != "" {
		cfg.WorkDir = v
	}

	if v := os.Getenv("RATE_LIMIT_RPM"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.RateLimiter.RPM = n
		} else {
			log.Printf("WARNING: ignoring invalid RATE_LIMIT_RPM %q: %v", v, err)
		}
	}
	if v := os.Getenv("RATE_LIMIT_ITPM"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.RateLimiter.ITPM = n
		} else {
			log.Printf("WARNING: ignoring invalid RATE_LIMIT_ITPM %q: %v", v, err)
		}
	}
	if v := os.Getenv("RATE_LIMIT_OTPM"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.RateLimiter.OTPM = n
		} else {
			log.Printf("WARNING: ignoring invalid RATE_LIMIT_OTPM %q: %v", v, err)
		}
	}

	if v := os.Getenv("DB_PATH"); v != "" {
		cfg.Database.Path = v
	}
	if v := os.Getenv("DASHBOARD_TOKEN"); v != "" {
		cfg.Dashboard.BearerToken = v
	}
	if v := os.Getenv("POSTGRES_URL"); v != "" {
		cfg.PostgresURL = v
	}
	if v := os.Getenv("OLLAMA_URL"); v != "" {
		cfg.OllamaURL = v
	}
	if v := os.Getenv("OLLAMA_EMBED_MODEL"); v != "" {
		cfg.OllamaEmbedModel = v
	}
	if v := os.Getenv("OLLAMA_LLM_MODEL"); v != "" {
		cfg.OllamaLLMModel = v
	}
}

// resolveInt converts val to int, returning fallback if val is nil or unconvertible.
// The parameter is typed as any because YAML unmarshaling produces mixed numeric types
// (int, int64, float64) and string values from environment variable substitution patterns.
func resolveInt(val any, fallback int) int {
	switch v := val.(type) {
	case int:
		return v
	case int64:
		if v > math.MaxInt || v < math.MinInt {
			return fallback
		}
		return int(v)
	case float64:
		if v > float64(math.MaxInt) || v < float64(math.MinInt) {
			return fallback
		}
		return int(v)
	case string:
		// Handle ${VAR:-default} patterns
		if strings.HasPrefix(v, "${") {
			v = strings.TrimPrefix(v, "${")
			v = strings.TrimSuffix(v, "}")
			parts := strings.SplitN(v, ":-", 2)
			envVal := os.Getenv(parts[0])
			if envVal != "" {
				if n, err := strconv.Atoi(envVal); err == nil {
					return n
				}
			}
			if len(parts) > 1 {
				if n, err := strconv.Atoi(parts[1]); err == nil {
					return n
				}
			}
		}
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}
