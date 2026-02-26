package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/you/claude-gateway/internal/agent"
	"github.com/you/claude-gateway/internal/bot"
	"github.com/you/claude-gateway/internal/config"
	"github.com/you/claude-gateway/internal/cron"
	"github.com/you/claude-gateway/internal/ctxbuilder"
	"github.com/you/claude-gateway/internal/dashboard"
	"github.com/you/claude-gateway/internal/db"
	"github.com/you/claude-gateway/internal/journal"
	"github.com/you/claude-gateway/internal/memory"
	"github.com/you/claude-gateway/internal/ollama"
	"github.com/you/claude-gateway/internal/orchestrator"
	"github.com/you/claude-gateway/internal/people"
	"github.com/you/claude-gateway/internal/pgdb"
	"github.com/you/claude-gateway/internal/profile"
	"github.com/you/claude-gateway/internal/ratelimiter"
	"github.com/you/claude-gateway/internal/skills"
	"github.com/you/claude-gateway/internal/triage"
	"github.com/you/claude-gateway/internal/watcher"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Println("Starting Claude Gateway v2...")

	// --- Config ---
	yamlPath := "configs/gateway.yaml"
	if v := os.Getenv("CONFIG_PATH"); v != "" {
		yamlPath = v
	}

	cfg, err := config.Load(yamlPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// --- Database ---
	database, err := db.Open(cfg.Database.Path)
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer database.Close()

	// --- PostgreSQL ---
	pgDB, err := pgdb.Open(cfg.PostgresURL)
	if err != nil {
		log.Fatalf("PostgreSQL connection failed: %v", err)
	}
	defer pgDB.Close()

	// --- Ollama ---
	ollamaClient := ollama.NewClient(cfg.OllamaURL, cfg.OllamaEmbedModel, cfg.OllamaLLMModel)
	ollamaHealthCtx, ollamaHealthCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer ollamaHealthCancel()
	if err := ollamaClient.Health(ollamaHealthCtx); err != nil {
		log.Printf("WARNING: Ollama unavailable: %v (triage + embeddings disabled)", err)
	} else {
		log.Println("Ollama is healthy.")
	}

	// --- Sidecar Client ---
	// HTTP timeout must accommodate both normal requests and long-running agent runs.
	// Use the longer of sidecar timeout and orchestrator timeout (+ buffer).
	const orchTimeoutBuffer = 30 // extra seconds for network/startup overhead
	timeout := time.Duration(cfg.Sidecar.TimeoutSeconds) * time.Second
	orchTimeout := time.Duration(cfg.Orchestrator.Timeout+orchTimeoutBuffer) * time.Second
	if orchTimeout > timeout {
		timeout = orchTimeout
	}
	agentClient := agent.NewClient(cfg.Sidecar.URL, timeout)

	// Wait for sidecar to be ready (use a temporary context for startup)
	log.Println("Waiting for sidecar...")
	startupCtx, startupCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer startupCancel()
	sidecarReady := waitForSidecar(startupCtx, agentClient)

	// --- Rate Limiter ---
	limiter := ratelimiter.New(
		cfg.RateLimiter.RPM,
		cfg.RateLimiter.ITPM,
		cfg.RateLimiter.OTPM,
		cfg.RateLimiter.SafetyMargin,
	)
	limiter.SetDB(database)

	// --- Skills Manager ---
	skillsMgr := skills.NewManager(database, cfg.Skills.Dirs)
	if err := skillsMgr.Start(); err != nil {
		log.Printf("Warning: skills manager: %v", err)
	}
	defer skillsMgr.Stop()

	// --- Memory Manager ---
	memMgr := memory.NewManager(database, cfg.Memory.BootstrapDir, cfg.Memory.DailyDir, cfg.Memory.MaxCuratedInject, cfg.Memory.MaxDailyDays, cfg.Memory.ClaudeMDPaths...)
	if err := memMgr.Start(startupCtx); err != nil {
		log.Printf("Warning: memory manager: %v", err)
	}
	memCompactor := memory.NewCompactor(memMgr)

	// --- User Profiler ---
	// Created early so profile doc sync can store condensed aspects.
	profiler := profile.NewProfiler(pgDB, ollamaClient)

	// --- Bootstrap Memory → PostgreSQL ---
	syncBootstrapMemory(pgDB, cfg.Memory.BootstrapDir)

	// --- Profile Documents → PostgreSQL ---
	syncProfileDocuments(pgDB, ollamaClient, profiler, cfg.Memory.ProfilesDir)

	// --- People Manager ---
	peopleMgr := people.NewManager(pgDB, ollamaClient)

	// --- Daily Journal ---
	journalMgr := journal.NewManager(pgDB, ollamaClient, ollamaClient)

	// --- Context Builder ---
	ctxBuilder := ctxbuilder.NewBuilder(pgDB, ollamaClient, peopleMgr, profiler, journalMgr, cfg.Context)

	// --- Triager ---
	triager := triage.NewTriager(ollamaClient)

	// --- Orchestrator Engine ---
	orchEngine := orchestrator.NewEngine(database, agentClient, cfg.Orchestrator.AgentDNADir, cfg.WorkDir, cfg.Orchestrator.Timeout)

	// --- Telegram Bot ---
	tgBot, err := bot.New(cfg, agentClient, limiter, database,
		bot.WithSkillsManager(skillsMgr),
		bot.WithMemoryManager(memMgr),
		bot.WithMemoryCompactor(memCompactor),
		bot.WithOrchestrator(orchEngine),
		bot.WithPgDB(pgDB),
		bot.WithContextBuilder(ctxBuilder),
		bot.WithPeopleManager(peopleMgr),
		bot.WithTriager(triager),
		bot.WithJournal(journalMgr),
		bot.WithProfiler(profiler),
		bot.WithSystemPromptBuilder(func(ctx context.Context) string {
			// Legacy fallback — used only when contextBuilder is nil
			var parts []string
			if section := memMgr.BuildSystemPromptSection(ctx); section != "" {
				parts = append(parts, section)
			}
			if section := skillsMgr.BuildSystemPromptSection(); section != "" {
				parts = append(parts, section)
			}
			if len(parts) == 0 {
				return ""
			}
			return strings.Join(parts, "\n\n")
		}),
	)
	if err != nil {
		log.Fatalf("Failed to create bot: %v", err)
	}

	// --- Cron Scheduler ---
	// Created after bot because it needs tgBot.SendNotification for alerts.
	// CronStatus is wired back via SetCronStatus to resolve the circular dependency.
	cronScheduler := cron.New(agentClient, limiter, tgBot.SendNotification, cfg.WorkDir)
	if cfg.Cron != nil {
		if err := cronScheduler.LoadJobs(cfg.Cron); err != nil {
			log.Fatalf("Failed to load cron jobs: %v", err)
		}
	}
	tgBot.SetCronStatus(cronScheduler.Status)
	tgBot.SetCronScheduler(cronScheduler)

	// --- Watchers ---
	watcherMgr := watcher.NewManager(agentClient, limiter, tgBot.SendNotification, cfg.WorkDir)
	watcherMgr.Configure(cfg.Watchers)

	// --- Heartbeat ---
	var heartbeatCancel context.CancelFunc
	if cfg.Heartbeat.Enabled {
		var heartbeatCtx context.Context
		heartbeatCtx, heartbeatCancel = context.WithCancel(context.Background())
		go runHeartbeat(heartbeatCtx, agentClient, tgBot, cfg.Heartbeat)
	}

	// --- Dashboard ---
	var dashSrv *dashboard.Server
	if cfg.Dashboard.Enabled {
		var dashErr error
		dashSrv, dashErr = dashboard.NewServer(cfg, database, limiter, skillsMgr, memMgr)
		if dashErr != nil {
			log.Fatalf("Failed to create dashboard: %v", dashErr)
		}
		if err := dashSrv.Start(); err != nil {
			log.Printf("Warning: dashboard: %v", err)
		}
	}

	// --- Start everything ---
	ctx, cancel := context.WithCancel(context.Background())

	cronScheduler.Start()
	watcherMgr.Start()

	// Track background goroutines so shutdown waits for them before closing the database.
	var wg sync.WaitGroup

	// Schedule daily memory compaction + journal generation at midnight
	wg.Add(1)
	go func() {
		defer wg.Done()
		runDailyCompaction(ctx, memCompactor, journalMgr, profiler)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		tgBot.Run(ctx)
	}()
	log.Println("Gateway is running. Ctrl+C to stop.")

	if cfg.Telegram.NotifyChatID != 0 {
		tgBot.SendNotification("Claude Gateway v2 started.")
		if !sidecarReady {
			tgBot.SendNotification("WARNING: Gateway started in degraded mode — sidecar was not ready at startup. Claude interactions may fail.")
		}
	}

	// --- Graceful shutdown ---
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Println("Shutting down...")

	if cfg.Telegram.NotifyChatID != 0 {
		tgBot.SendNotification("Claude Gateway v2 shutting down.")
	}

	// Cancel context to signal bot and heartbeat to stop
	cancel()
	cronScheduler.Stop()
	watcherMgr.Stop()
	if heartbeatCancel != nil {
		heartbeatCancel()
	}

	// Shut down dashboard server gracefully
	if dashSrv != nil {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		if err := dashSrv.Shutdown(shutdownCtx); err != nil {
			log.Printf("Dashboard shutdown error: %v", err)
		}
	}

	// Wait for bot and compaction goroutines to finish.
	wg.Wait()

	// Wait for bot's fire-and-forget background goroutines (RecordExchange,
	// fact extraction, profile update) before closing resources they depend on.
	tgBot.WaitBackground()

	// Close the sidecar WebSocket client after all handlers have finished,
	// so queued requests using context.Background() don't hit a closed connection.
	agentClient.Close()

	limiter.Close()

	log.Println("Goodbye.")
}

func waitForSidecar(ctx context.Context, client *agent.Client) bool {
	for {
		if err := client.Health(ctx); err == nil {
			log.Println("Sidecar is ready.")
			return true
		}
		select {
		case <-time.After(2 * time.Second):
		case <-ctx.Done():
			log.Println("WARNING: sidecar not ready after timeout, proceeding in degraded mode")
			return false
		}
	}
}

func runDailyCompaction(ctx context.Context, compactor *memory.Compactor, journalMgr *journal.Manager, profiler *profile.Profiler) {
	for {
		// Calculate time until next midnight to avoid ticker drift.
		now := time.Now()
		next := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, now.Location())
		select {
		case <-time.After(time.Until(next)):
		case <-ctx.Done():
			return
		}

		// Guard against select non-determinism: the timer and ctx.Done()
		// may both be ready simultaneously, and Go selects randomly.
		if ctx.Err() != nil {
			return
		}

		compactor.RunDailyCompaction(ctx)

		// Generate daily journal for the day that just ended (yesterday).
		// After midnight, time.Now() is the new day; we want the previous day.
		if journalMgr != nil {
			yesterday := next.Add(-24 * time.Hour).Format("2006-01-02")
			entry, err := journalMgr.GenerateDailySummary(ctx, yesterday)
			if err != nil {
				log.Printf("Daily journal generation failed: %v", err)
			} else if entry != nil && profiler != nil {
				// Update user profile from journal patterns
				if profErr := profiler.UpdateFromJournal(ctx, entry); profErr != nil {
					log.Printf("Profile journal update failed: %v", profErr)
				}
			}
		}
	}
}

func runHeartbeat(ctx context.Context, client *agent.Client, tgBot *bot.Bot, cfg config.Heartbeat) {
	interval := time.Duration(cfg.IntervalSeconds) * time.Second
	failures := 0

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			err := client.Health(ctx)
			if err != nil {
				failures++
				log.Printf("Heartbeat failure #%d: %v", failures, err)

				if failures >= cfg.MaxFailures {
					tgBot.SendNotification(
						fmt.Sprintf("Sidecar heartbeat failed %d times: %v", failures, err),
					)
				}
			} else {
				if failures > 0 {
					log.Printf("Heartbeat recovered after %d failures", failures)
				}
				failures = 0
			}
		}
	}
}

// syncBootstrapMemory reads .md files from the bootstrap directory and upserts
// them into PostgreSQL memory_entries with category='bootstrap'. This ensures
// the v2 context builder always has the environment info, tool docs, and skill
// detection rules — even on a fresh database.
// Files are keyed by filename; content is updated if it changes on disk.
func syncBootstrapMemory(db *pgdb.DB, bootstrapDir string) {
	if bootstrapDir == "" {
		return
	}

	files, err := filepath.Glob(filepath.Join(bootstrapDir, "*.md"))
	if err != nil {
		log.Printf("Bootstrap sync: glob failed: %v", err)
		return
	}
	if len(files) == 0 {
		log.Printf("Bootstrap sync: no .md files in %s", bootstrapDir)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Load existing bootstrap entries for comparison
	existing, err := db.GetMemoryByCategory(ctx, "bootstrap")
	if err != nil {
		log.Printf("Bootstrap sync: failed to query existing entries: %v", err)
		return
	}
	existingByKey := make(map[string]pgdb.MemoryEntry, len(existing))
	for _, e := range existing {
		existingByKey[e.Key] = e
	}

	synced := 0
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			log.Printf("Bootstrap sync: failed to read %s: %v", f, err)
			continue
		}
		content := strings.TrimSpace(string(data))
		if content == "" {
			continue
		}

		key := filepath.Base(f)
		if entry, ok := existingByKey[key]; ok {
			// Update if content changed
			if entry.Content != content {
				if err := db.UpdateMemoryEntry(ctx, entry.ID, content, 10); err != nil {
					log.Printf("Bootstrap sync: failed to update %s: %v", key, err)
				} else {
					synced++
				}
			}
			delete(existingByKey, key)
		} else {
			// Insert new
			if _, err := db.SaveMemoryEntry(ctx, "bootstrap", key, content, "file", 10); err != nil {
				log.Printf("Bootstrap sync: failed to insert %s: %v", key, err)
			} else {
				synced++
			}
		}
	}

	if synced > 0 {
		log.Printf("Bootstrap sync: %d files synced to PostgreSQL", synced)
	} else {
		log.Printf("Bootstrap sync: %d files up to date", len(files))
	}
}

// syncProfileDocuments reads .md files from profile subdirectories and:
// 1. Upserts full content into memory_entries with category='profile_doc'
// 2. Uses Ollama 7B to generate a condensed summary as a profile aspect
// Directory structure: profiles/<username>/<doc>.md
func syncProfileDocuments(db *pgdb.DB, llm *ollama.Client, profiler *profile.Profiler, profilesDir string) {
	if profilesDir == "" {
		return
	}

	userDirs, err := os.ReadDir(profilesDir)
	if err != nil {
		log.Printf("Profile doc sync: cannot read %s: %v", profilesDir, err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Load existing profile_doc entries for comparison
	existing, err := db.GetMemoryByCategory(ctx, "profile_doc")
	if err != nil {
		log.Printf("Profile doc sync: failed to query existing entries: %v", err)
		return
	}
	existingByKey := make(map[string]pgdb.MemoryEntry, len(existing))
	for _, e := range existing {
		existingByKey[e.Key] = e
	}

	synced := 0
	for _, userDir := range userDirs {
		if !userDir.IsDir() {
			continue
		}
		username := userDir.Name()
		userPath := filepath.Join(profilesDir, username)

		files, err := filepath.Glob(filepath.Join(userPath, "*.md"))
		if err != nil {
			log.Printf("Profile doc sync: glob failed for %s: %v", userPath, err)
			continue
		}

		for _, f := range files {
			data, err := os.ReadFile(f)
			if err != nil {
				log.Printf("Profile doc sync: failed to read %s: %v", f, err)
				continue
			}
			content := strings.TrimSpace(string(data))
			if content == "" {
				continue
			}

			filename := filepath.Base(f)
			key := username + "/" + filename

			if entry, ok := existingByKey[key]; ok {
				if entry.Content != content {
					if err := db.UpdateMemoryEntry(ctx, entry.ID, content, 9); err != nil {
						log.Printf("Profile doc sync: failed to update %s: %v", key, err)
					} else {
						synced++
						condenseProfileDoc(ctx, llm, profiler, key, filename, content)
					}
				}
				delete(existingByKey, key)
			} else {
				if _, err := db.SaveMemoryEntry(ctx, "profile_doc", key, content, "file", 9); err != nil {
					log.Printf("Profile doc sync: failed to insert %s: %v", key, err)
				} else {
					synced++
					condenseProfileDoc(ctx, llm, profiler, key, filename, content)
				}
			}
		}
	}

	if synced > 0 {
		log.Printf("Profile doc sync: %d documents synced to PostgreSQL", synced)
	} else {
		log.Printf("Profile doc sync: all documents up to date")
	}
}

// condenseProfileDoc uses Ollama 7B to generate a condensed summary of a profile
// document and stores it as a profile aspect at confidence 1.0.
// Degrades gracefully if Ollama or profiler is unavailable.
func condenseProfileDoc(ctx context.Context, llm *ollama.Client, profiler *profile.Profiler, key, filename, content string) {
	if llm == nil || profiler == nil {
		log.Printf("Profile doc sync: skipping condensation for %s (Ollama or profiler unavailable)", key)
		return
	}

	aspectName := "doc:" + strings.TrimSuffix(filename, ".md")

	// Truncate for the 7B model — first 6KB covers the most important sections
	truncated := content
	if len(truncated) > 6000 {
		truncated = truncated[:6000]
	}

	systemPrompt := `Summarize this writing style analysis into a concise profile (200-300 words).
Focus on:
- Overall tone and formality level
- Key phrases and patterns (greetings, closings, sign-offs)
- Structural preferences (paragraph style, sentence length)
- Any domain-specific conventions

Return ONLY the summary text, no JSON, no markdown headers.`

	summary, err := llm.Generate(ctx, truncated, systemPrompt)
	if err != nil {
		log.Printf("Profile doc sync: condensation failed for %s: %v", key, err)
		return
	}

	summary = strings.TrimSpace(summary)
	if summary == "" {
		log.Printf("Profile doc sync: empty condensation for %s", key)
		return
	}

	// Append reference to the full document
	summary += fmt.Sprintf("\n\n[Full analysis available in profile document '%s'. Read this document before executing tasks related to this aspect.]", filename)

	if err := profiler.SetAspect(ctx, aspectName, summary); err != nil {
		log.Printf("Profile doc sync: failed to set aspect %s: %v", aspectName, err)
	} else {
		log.Printf("Profile doc sync: condensed %s → aspect %s (%d chars)", key, aspectName, len(summary))
	}
}
