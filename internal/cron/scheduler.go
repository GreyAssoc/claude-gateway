// Package cron schedules recurring Claude prompts on cron expressions.
package cron

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/you/claude-gateway/internal/agent"
	"github.com/you/claude-gateway/internal/config"
	"github.com/you/claude-gateway/internal/ratelimiter"
)

// NotifyFunc is called with the cron output to send to the user.
type NotifyFunc func(text string)

// Scheduler manages cron jobs that invoke Claude.
type Scheduler struct {
	cron    *cron.Cron
	agent   *agent.Client
	limiter *ratelimiter.Limiter
	notify  NotifyFunc
	workDir string

	mu   sync.RWMutex
	jobs map[string]*jobInfo

	// done is closed on Stop() to signal running jobs to cancel.
	done     chan struct{}
	doneOnce sync.Once
}

type jobInfo struct {
	Name     string
	Schedule string
	Prompt   string
	Enabled  bool
	EntryID  cron.EntryID
}

// New creates a cron scheduler that executes Claude prompts on a schedule.
func New(agentClient *agent.Client, limiter *ratelimiter.Limiter, notify NotifyFunc, workDir string) *Scheduler {
	return &Scheduler{
		cron:    cron.New(),
		agent:   agentClient,
		limiter: limiter,
		notify:  notify,
		workDir: workDir,
		jobs:    make(map[string]*jobInfo),
		done:    make(chan struct{}),
	}
}

// LoadJobs registers cron jobs from config.
func (s *Scheduler) LoadJobs(cronJobs map[string]config.CronJob) error {
	for name, job := range cronJobs {
		if !job.Enabled {
			s.jobs[name] = &jobInfo{
				Name:     name,
				Schedule: job.Schedule,
				Prompt:   job.Prompt,
				Enabled:  false,
			}
			continue
		}

		if err := s.addJob(name, job); err != nil {
			return fmt.Errorf("adding cron job %q: %w", name, err)
		}
	}
	return nil
}

func (s *Scheduler) addJob(name string, job config.CronJob) error {
	prompt := strings.TrimSpace(job.Prompt)
	jobName := name // capture for closure

	entryID, err := s.cron.AddFunc(job.Schedule, func() {
		s.executeJob(jobName, prompt)
	})
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.jobs[name] = &jobInfo{
		Name:     name,
		Schedule: job.Schedule,
		Prompt:   prompt,
		Enabled:  true,
		EntryID:  entryID,
	}
	s.mu.Unlock()

	log.Printf("Cron job %q registered: %s", name, job.Schedule)
	return nil
}

// internalPrefix marks prompts that should be handled by gateway internals,
// not sent to Claude. Example: "__INTERNAL_MEMORY_COMPACT__".
const internalPrefix = "__INTERNAL_"

// cronSystemPrompt gives Claude context about available tools when executing
// cron jobs. Without this, Claude doesn't know about the gog CLI or other
// tools available in the sidecar container.
const cronSystemPrompt = `You are executing a scheduled cron job for the user's personal gateway.
You have access to these tools via Bash:
- gog: Google Workspace CLI (Gmail, Calendar, Drive, Sheets)
  Examples:
    gog gmail search --query "is:unread newer_than:12h" --max 20
    gog gmail read <message-id>
    gog calendar list --days 1
    gog calendar list --days 7
    gog drive list --folder "Projects"
    gog sheets read <spreadsheet-id> <range>

Keep your response concise and well-formatted for Telegram. Use bullet points.
If a tool is unavailable or returns an error, note it briefly and continue with what you can do.`

func (s *Scheduler) executeJob(name, prompt string) {
	// Skip if scheduler is stopping
	select {
	case <-s.done:
		log.Printf("Cron %s skipped: scheduler stopping", name)
		return
	default:
	}

	// Skip internal sentinel prompts — these are handled elsewhere.
	if strings.HasPrefix(prompt, internalPrefix) {
		log.Printf("Cron %s skipped: internal sentinel %q", name, prompt)
		return
	}

	log.Printf("Cron executing: %s", name)

	// Check rate limits before running
	if !s.limiter.CanProceed(2000) {
		log.Printf("Cron %s skipped: rate limited", name)
		if s.notify != nil {
			s.notify(fmt.Sprintf("⏭️ Cron job *%s* skipped — rate limited.", name))
		}
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// Cancel job early if scheduler stops during execution.
	// The goroutine always exits when this function returns because
	// defer cancel() closes ctx.Done(), which the select watches.
	go func() {
		select {
		case <-s.done:
			cancel()
		case <-ctx.Done():
		}
	}()

	sessionID := fmt.Sprintf("cron-%s", name)

	resp, err := s.agent.AskWithOptions(ctx, agent.AskOptions{
		SessionID:    sessionID,
		Prompt:       prompt,
		WorkDir:      s.workDir,
		MaxTurns:     20,
		SystemPrompt: cronSystemPrompt,
	})
	if err != nil {
		log.Printf("Cron %s failed: %v", name, err)
		if s.notify != nil {
			s.notify(fmt.Sprintf("❌ Cron job *%s* failed: %v", name, err))
		}
		return
	}

	if resp.Usage != nil {
		s.limiter.Record(resp.Usage)
	}

	text := resp.Text
	if text == "" {
		text = "(no output)"
	}

	if s.notify != nil {
		s.notify(fmt.Sprintf("🕐 *%s*\n\n%s", name, text))
	}
}

// Start begins the cron scheduler.
func (s *Scheduler) Start() {
	s.cron.Start()
	log.Printf("Cron scheduler started with %d jobs", len(s.cron.Entries()))
}

// Stop signals running jobs to cancel and waits for them to finish.
// Blocks for at most 30 seconds to avoid hanging shutdown.
func (s *Scheduler) Stop() {
	s.doneOnce.Do(func() { close(s.done) })
	ctx := s.cron.Stop()
	select {
	case <-ctx.Done():
	case <-time.After(30 * time.Second):
		log.Println("Cron: force-stopped after 30s waiting for in-flight jobs")
	}
}

// AddDynamic registers a new cron job at runtime. Returns an error if the
// name already exists or the schedule expression is invalid.
func (s *Scheduler) AddDynamic(name, schedule, prompt string) error {
	s.mu.Lock()
	if _, exists := s.jobs[name]; exists {
		s.mu.Unlock()
		return fmt.Errorf("job %q already exists", name)
	}
	s.mu.Unlock()

	return s.addJob(name, config.CronJob{
		Schedule: schedule,
		Prompt:   prompt,
		Enabled:  true,
	})
}

// RemoveJob removes a cron job by name.
func (s *Scheduler) RemoveJob(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	job, ok := s.jobs[name]
	if !ok {
		return fmt.Errorf("job %q not found", name)
	}
	if job.Enabled {
		s.cron.Remove(job.EntryID)
	}
	delete(s.jobs, name)
	log.Printf("Cron job %q removed", name)
	return nil
}

// DisableJob pauses a running cron job without removing it.
func (s *Scheduler) DisableJob(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	job, ok := s.jobs[name]
	if !ok {
		return fmt.Errorf("job %q not found", name)
	}
	if !job.Enabled {
		return fmt.Errorf("job %q is already disabled", name)
	}
	s.cron.Remove(job.EntryID)
	job.Enabled = false
	log.Printf("Cron job %q disabled", name)
	return nil
}

// EnableJob resumes a disabled cron job.
func (s *Scheduler) EnableJob(name string) error {
	s.mu.Lock()
	job, ok := s.jobs[name]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("job %q not found", name)
	}
	if job.Enabled {
		s.mu.Unlock()
		return fmt.Errorf("job %q is already enabled", name)
	}
	schedule := job.Schedule
	prompt := job.Prompt
	s.mu.Unlock()

	entryID, err := s.cron.AddFunc(schedule, func() {
		s.executeJob(name, prompt)
	})
	if err != nil {
		return fmt.Errorf("re-enabling job %q: %w", name, err)
	}

	s.mu.Lock()
	job.EntryID = entryID
	job.Enabled = true
	s.mu.Unlock()
	log.Printf("Cron job %q enabled", name)
	return nil
}

// ListJobs returns job details for external use.
func (s *Scheduler) ListJobs() []JobDetail {
	s.mu.RLock()
	defer s.mu.RUnlock()

	details := make([]JobDetail, 0, len(s.jobs))
	for _, job := range s.jobs {
		details = append(details, JobDetail{
			Name:     job.Name,
			Schedule: job.Schedule,
			Prompt:   job.Prompt,
			Enabled:  job.Enabled,
		})
	}
	return details
}

// JobDetail is an exported view of a cron job.
type JobDetail struct {
	Name     string
	Schedule string
	Prompt   string
	Enabled  bool
}

// Status returns a formatted string of all jobs with next-run times.
func (s *Scheduler) Status() string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.jobs) == 0 {
		return "No cron jobs configured."
	}

	// Build entry ID → next-run lookup from the cron library
	nextRun := make(map[cron.EntryID]time.Time)
	for _, entry := range s.cron.Entries() {
		nextRun[entry.ID] = entry.Next
	}

	var lines []string
	lines = append(lines, "📋 *Scheduled Jobs*\n")

	for _, job := range s.jobs {
		icon := "✅"
		nextStr := ""
		if !job.Enabled {
			icon = "⏸️"
			nextStr = " (disabled)"
		} else if t, ok := nextRun[job.EntryID]; ok && !t.IsZero() {
			nextStr = fmt.Sprintf(" → next: %s", t.Format("Mon 15:04"))
		}

		// Truncate prompt for display
		prompt := job.Prompt
		if len(prompt) > 60 {
			prompt = prompt[:57] + "..."
		}
		prompt = strings.ReplaceAll(prompt, "\n", " ")

		lines = append(lines, fmt.Sprintf("%s *%s* — `%s`%s\n   %s", icon, job.Name, job.Schedule, nextStr, prompt))
	}

	return strings.Join(lines, "\n")
}
