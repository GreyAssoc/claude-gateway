package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Telegram    TelegramConfig
	Sidecar     SidecarConfig
	RateLimiter RateLimiterConfig
	Cron        map[string]CronJob
	Watchers    WatchersConfig
	Heartbeat   HeartbeatConfig
	Database    DatabaseConfig
	Skills      SkillsConfig
	Memory      MemoryConfig
	Orchestrator OrchestratorConfig
	Dashboard   DashboardConfig
	WorkDir     string
}

type TelegramConfig struct {
	BotToken       string
	AllowedUserIDs map[int64]bool
	NotifyChatID   int64
}

type SidecarConfig struct {
	URL            string
	TimeoutSeconds int
	HealthPath     string
}

type RateLimiterConfig struct {
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

type HeartbeatConfig struct {
	Enabled         bool `yaml:"enabled"`
	IntervalSeconds int  `yaml:"interval_seconds"`
	MaxFailures     int  `yaml:"max_failures"`
}

type DatabaseConfig struct {
	Path string `yaml:"path"`
}

type SkillsConfig struct {
	Dirs []string `yaml:"dirs"`
}

type MemoryConfig struct {
	BootstrapDir     string `yaml:"bootstrap_dir"`
	DailyDir         string `yaml:"daily_dir"`
	MaxCuratedInject int    `yaml:"max_curated_inject"`
	MaxDailyDays     int    `yaml:"max_daily_days"`
}

type OrchestratorConfig struct {
	AgentDNADir string `yaml:"agent_dna_dir"`
	Timeout     int    `yaml:"timeout_seconds"`
}

type DashboardConfig struct {
	Enabled  bool   `yaml:"enabled"`
	Port     int    `yaml:"port"`
	BearerToken string
}

// rawConfig mirrors the YAML structure before env var resolution
type rawConfig struct {
	Cron      map[string]CronJob `yaml:"cron"`
	Watchers  WatchersConfig     `yaml:"watchers"`
	Heartbeat HeartbeatConfig    `yaml:"heartbeat"`
	Sidecar   struct {
		URL            string `yaml:"url"`
		TimeoutSeconds int    `yaml:"timeout_seconds"`
		HealthPath     string `yaml:"health_path"`
	} `yaml:"sidecar"`
	RateLimiter struct {
		RPM          interface{} `yaml:"rpm"`
		ITPM         interface{} `yaml:"itpm"`
		OTPM         interface{} `yaml:"otpm"`
		SafetyMargin float64     `yaml:"safety_margin"`
	} `yaml:"ratelimiter"`
	Database     DatabaseConfig     `yaml:"database"`
	Skills       SkillsConfig       `yaml:"skills"`
	Memory       MemoryConfig       `yaml:"memory"`
	Orchestrator OrchestratorConfig `yaml:"orchestrator"`
	Dashboard    DashboardConfig    `yaml:"dashboard"`
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

	// Defaults
	if cfg.Sidecar.URL == "" {
		cfg.Sidecar.URL = "http://sidecar:3001"
	}
	if cfg.Sidecar.TimeoutSeconds == 0 {
		cfg.Sidecar.TimeoutSeconds = 300
	}
	if cfg.Sidecar.HealthPath == "" {
		cfg.Sidecar.HealthPath = "/health"
	}
	if cfg.WorkDir == "" {
		cfg.WorkDir = "/home/user/project"
	}
	if cfg.RateLimiter.SafetyMargin == 0 {
		cfg.RateLimiter.SafetyMargin = 0.85
	}
	if cfg.Heartbeat.IntervalSeconds == 0 {
		cfg.Heartbeat.IntervalSeconds = 300
	}
	if cfg.Heartbeat.MaxFailures == 0 {
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
	if cfg.Memory.DailyDir == "" {
		cfg.Memory.DailyDir = "memory/daily"
	}
	if cfg.Memory.MaxCuratedInject == 0 {
		cfg.Memory.MaxCuratedInject = 20
	}
	if cfg.Memory.MaxDailyDays == 0 {
		cfg.Memory.MaxDailyDays = 7
	}
	if cfg.Orchestrator.AgentDNADir == "" {
		cfg.Orchestrator.AgentDNADir = "/app/agents"
	}
	if cfg.Orchestrator.Timeout == 0 {
		cfg.Orchestrator.Timeout = 600
	}
	if cfg.Dashboard.Port == 0 {
		cfg.Dashboard.Port = 8080
	}

	return cfg, nil
}

func loadYAML(path string, cfg *Config) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	var raw rawConfig
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return err
	}

	cfg.Cron = raw.Cron
	cfg.Watchers = raw.Watchers
	cfg.Heartbeat = raw.Heartbeat

	cfg.Sidecar.URL = raw.Sidecar.URL
	cfg.Sidecar.TimeoutSeconds = raw.Sidecar.TimeoutSeconds
	cfg.Sidecar.HealthPath = raw.Sidecar.HealthPath

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
			if id, err := strconv.ParseInt(idStr, 10, 64); err == nil {
				cfg.Telegram.AllowedUserIDs[id] = true
			}
		}
	}

	if v := os.Getenv("TELEGRAM_CHAT_ID"); v != "" {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			cfg.Telegram.NotifyChatID = id
		}
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
		}
	}
	if v := os.Getenv("RATE_LIMIT_ITPM"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.RateLimiter.ITPM = n
		}
	}
	if v := os.Getenv("RATE_LIMIT_OTPM"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.RateLimiter.OTPM = n
		}
	}

	if v := os.Getenv("DB_PATH"); v != "" {
		cfg.Database.Path = v
	}
	if v := os.Getenv("DASHBOARD_TOKEN"); v != "" {
		cfg.Dashboard.BearerToken = v
	}
}

func resolveInt(val interface{}, fallback int) int {
	switch v := val.(type) {
	case int:
		return v
	case float64:
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
