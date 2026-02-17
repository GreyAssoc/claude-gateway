package cron

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"

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
}

type jobInfo struct {
	Name     string
	Schedule string
	Prompt   string
	Enabled  bool
	EntryID  cron.EntryID
	Paused   bool
}

func New(agentClient *agent.Client, limiter *ratelimiter.Limiter, notify NotifyFunc, workDir string) *Scheduler {
	return &Scheduler{
		cron:    cron.New(),
		agent:   agentClient,
		limiter: limiter,
		notify:  notify,
		workDir: workDir,
		jobs:    make(map[string]*jobInfo),
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

func (s *Scheduler) executeJob(name, prompt string) {
	log.Printf("Cron executing: %s", name)

	// Check rate limits before running
	if !s.limiter.CanProceed(2000) {
		log.Printf("Cron %s skipped: rate limited", name)
		s.notify(fmt.Sprintf("⏭️ Cron job *%s* skipped — rate limited.", name))
		return
	}

	ctx := context.Background()
	sessionID := fmt.Sprintf("cron-%s", name)

	resp, err := s.agent.Ask(ctx, sessionID, prompt, s.workDir, 5)
	if err != nil {
		log.Printf("Cron %s failed: %v", name, err)
		s.notify(fmt.Sprintf("❌ Cron job *%s* failed: %v", name, err))
		return
	}

	if resp.Usage != nil {
		s.limiter.Record(resp.Usage)
	}

	text := resp.Text
	if text == "" {
		text = "(no output)"
	}

	s.notify(fmt.Sprintf("🕐 *%s*\n\n%s", name, text))
}

// Start begins the cron scheduler.
func (s *Scheduler) Start() {
	s.cron.Start()
	log.Printf("Cron scheduler started with %d jobs", len(s.cron.Entries()))
}

// Stop halts the cron scheduler.
func (s *Scheduler) Stop() {
	s.cron.Stop()
}

// Status returns a formatted string of all jobs.
func (s *Scheduler) Status() string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.jobs) == 0 {
		return "No cron jobs configured."
	}

	var lines []string
	lines = append(lines, "📋 *Scheduled Jobs*\n")

	for _, job := range s.jobs {
		icon := "✅"
		if !job.Enabled {
			icon = "⏸️"
		}
		if job.Paused {
			icon = "⏸️"
		}
		lines = append(lines, fmt.Sprintf("%s *%s* — `%s`", icon, job.Name, job.Schedule))
	}

	return strings.Join(lines, "\n")
}
