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

// RecordFunc persists a cron exchange to PostgreSQL (session_id cron-<job>).
type RecordFunc func(ctx context.Context, sessionID, userMessage, assistantResponse string)

// Scheduler manages cron jobs that invoke Claude.
type Scheduler struct {
	cron    *cron.Cron
	agent   agent.Backend
	limiter *ratelimiter.Limiter
	notify  NotifyFunc
	record  RecordFunc
	prepare PrepareFunc
	workDir string

	mu   sync.RWMutex
	jobs map[string]*jobInfo

	// done is closed on Stop() to signal running jobs to cancel.
	done     chan struct{}
	doneOnce sync.Once
}

type jobInfo struct {
	Name      string
	Schedule  string
	Prompt    string
	Agent     string
	Framework string
	Model     string
	Enabled   bool
	EntryID   cron.EntryID
}

// ExecSpec is how one fire of a job talks to the sidecar.
type ExecSpec struct {
	Backend      agent.Backend
	WorkDir      string
	Model        string
	SystemPrompt string
	SessionID    string
	AgentDisplay string
	ModelLabel   string
}

// PrepareFunc binds a job to a specialist, backend, and vendor model.
type PrepareFunc func(name string, job config.CronJob) ExecSpec

// New creates a cron scheduler that executes Claude prompts on a schedule.
func New(agentClient agent.Backend, limiter *ratelimiter.Limiter, notify NotifyFunc, workDir string) *Scheduler {
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

// SetRecorder stores cron prompt/reply pairs in PostgreSQL under agent "cron".
func (s *Scheduler) SetRecorder(fn RecordFunc) {
	s.record = fn
}

// SetPrepare routes each job to an @agent and model. Nil keeps CRON_FRAMEWORK.
func (s *Scheduler) SetPrepare(fn PrepareFunc) {
	s.prepare = fn
}

// LoadJobs registers cron jobs from config.
func (s *Scheduler) LoadJobs(cronJobs map[string]config.CronJob) error {
	for name, job := range cronJobs {
		if !job.Enabled {
			s.jobs[name] = &jobInfo{
				Name:      name,
				Schedule:  job.Schedule,
				Prompt:    job.Prompt,
				Agent:     strings.TrimSpace(strings.TrimPrefix(job.Agent, "@")),
				Framework: strings.TrimSpace(job.Framework),
				Model:     strings.TrimSpace(job.Model),
				Enabled:   false,
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
		s.executeJob(jobName)
	})
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.jobs[name] = &jobInfo{
		Name:      name,
		Schedule:  job.Schedule,
		Prompt:    prompt,
		Agent:     strings.TrimSpace(strings.TrimPrefix(job.Agent, "@")),
		Framework: strings.TrimSpace(job.Framework),
		Model:     strings.TrimSpace(job.Model),
		Enabled:   true,
		EntryID:   entryID,
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
Google Workspace is a harness skill — use it. Prefer named tools if you have them
(GmailSearch, GmailThread, CalendarList, DriveSearch, SheetsRead). Otherwise Bash gog:
    gog gmail search 'is:unread newer_than:12h' --max 20
    gog gmail thread <message-id>
    gog calendar list
    gog calendar list --from tomorrow --to +7d
    gog drive search 'name contains "Projects"'
    gog sheets read <spreadsheet-id> --range 'Sheet1!A1:Z100'
Never delete or trash mail. If gog says not authenticated, tell the user to run /googleauth.

Keep your response concise and well-formatted for Telegram. Use bullet points.
If a tool is unavailable or returns an error, note it briefly and continue with what you can do.`

func (s *Scheduler) executeJob(name string) {
	select {
	case <-s.done:
		log.Printf("Cron %s skipped: scheduler stopping", name)
		return
	default:
	}

	s.mu.RLock()
	stored, ok := s.jobs[name]
	s.mu.RUnlock()
	if !ok {
		log.Printf("Cron %s skipped: unknown job", name)
		return
	}
	prompt := stored.Prompt
	cfgJob := config.CronJob{
		Schedule:  stored.Schedule,
		Prompt:    stored.Prompt,
		Enabled:   true,
		Agent:     stored.Agent,
		Framework: stored.Framework,
		Model:     stored.Model,
	}

	if strings.HasPrefix(prompt, internalPrefix) {
		log.Printf("Cron %s skipped: internal sentinel %q", name, prompt)
		return
	}

	spec := s.specFor(name, cfgJob)
	log.Printf("Cron executing: %s agent=%s model=%s cwd=%s", name, spec.AgentDisplay, spec.Model, spec.WorkDir)

	paid := spec.Backend != nil && spec.Backend.Name() != "qwen" && spec.Backend.Name() != "vllm"
	if paid && !s.limiter.CanProceed(2000) {
		log.Printf("Cron %s skipped: rate limited", name)
		if s.notify != nil {
			s.notify(fmt.Sprintf("⏭️ Cron job *%s* skipped — rate limited.", name))
		}
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	go func() {
		select {
		case <-s.done:
			cancel()
		case <-ctx.Done():
		}
	}()

	sessionID := spec.SessionID
	if sessionID == "" {
		sessionID = fmt.Sprintf("cron-%s", name)
	}
	sys := spec.SystemPrompt
	if sys == "" {
		sys = cronSystemPrompt
	}
	be := spec.Backend
	if be == nil {
		be = s.agent
	}
	workDir := spec.WorkDir
	if workDir == "" {
		workDir = s.workDir
	}

	resp, err := be.AskWithOptions(ctx, agent.AskOptions{
		SessionID:    sessionID,
		Prompt:       prompt,
		WorkDir:      workDir,
		Model:        spec.Model,
		MaxTurns:     20,
		SystemPrompt: sys,
	})
	if err != nil {
		log.Printf("Cron %s failed: %v", name, err)
		if s.notify != nil {
			s.notify(fmt.Sprintf("❌ Cron job *%s* failed: %v", name, err))
		}
		return
	}

	if resp.Usage != nil && paid {
		s.limiter.Record(resp.Usage)
	}

	text := resp.Text
	if text == "" {
		text = "(no output)"
	}

	if looksLikeClaudeLogin(text) {
		log.Printf("Cron %s failed: Claude Code not logged in", name)
		if s.notify != nil {
			s.notify(fmt.Sprintf(
				"❌ Cron job *%s* failed: Claude Code in the sidecar is not logged in.\n\n"+
					"`/login` is a *Claude CLI* command, not Telegram. This job's framework is "+
					"Qwen/Claude unless the job sets `framework:` / `/cronadd … qwen|deepseek-flash|…`. "+
					"If it hit Claude by mistake, set `framework: qwen` on the job and check vLLM.",
				name,
			))
		}
		return
	}

	if s.record != nil {
		s.record(ctx, sessionID, prompt, text)
	}

	if s.notify != nil {
		title := fmt.Sprintf("🕐 *%s*", name)
		if spec.AgentDisplay != "" || spec.ModelLabel != "" {
			extra := spec.AgentDisplay
			if extra != "" {
				extra = "@" + extra
			}
			if spec.ModelLabel != "" {
				if extra != "" {
					extra += " · "
				}
				extra += spec.ModelLabel
			}
			title = fmt.Sprintf("🕐 *%s* (%s)", name, extra)
		}
		s.notify(fmt.Sprintf("%s\n\n%s", title, text))
	}
}

func (s *Scheduler) specFor(name string, job config.CronJob) ExecSpec {
	spec := ExecSpec{
		Backend:      s.agent,
		WorkDir:      s.workDir,
		SystemPrompt: cronSystemPrompt,
		SessionID:    fmt.Sprintf("cron-%s", name),
	}
	if s.prepare != nil {
		got := s.prepare(name, job)
		if got.Backend != nil {
			spec.Backend = got.Backend
		}
		if got.WorkDir != "" {
			spec.WorkDir = got.WorkDir
		}
		if got.SystemPrompt != "" {
			spec.SystemPrompt = got.SystemPrompt
		}
		if got.SessionID != "" {
			spec.SessionID = got.SessionID
		}
		spec.Model = got.Model
		spec.AgentDisplay = got.AgentDisplay
		spec.ModelLabel = got.ModelLabel
	}
	return spec
}

func looksLikeClaudeLogin(text string) bool {
	t := strings.ToLower(text)
	return strings.Contains(t, "not logged in") ||
		strings.Contains(t, "please run /login") ||
		strings.Contains(t, "please run `/login`")
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
func (s *Scheduler) AddDynamic(name string, job config.CronJob) error {
	s.mu.Lock()
	if _, exists := s.jobs[name]; exists {
		s.mu.Unlock()
		return fmt.Errorf("job %q already exists", name)
	}
	s.mu.Unlock()

	job.Enabled = true
	return s.addJob(name, job)
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
	s.mu.Unlock()

	entryID, err := s.cron.AddFunc(schedule, func() {
		s.executeJob(name)
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

// RunJob fires a configured job immediately in a goroutine. Disabled jobs
// can still be run this way. Internal sentinel jobs are rejected.
func (s *Scheduler) RunJob(name string) error {
	s.mu.RLock()
	job, ok := s.jobs[name]
	s.mu.RUnlock()
	if !ok {
		return fmt.Errorf("unknown job %q — try /cronlist", name)
	}
	if strings.HasPrefix(job.Prompt, internalPrefix) {
		return fmt.Errorf("job %q is internal and cannot be run from Telegram", name)
	}
	log.Printf("Cron %s triggered manually", name)
	go s.executeJob(job.Name)
	return nil
}

// ListJobs returns job details for external use.
func (s *Scheduler) ListJobs() []JobDetail {
	s.mu.RLock()
	defer s.mu.RUnlock()

	details := make([]JobDetail, 0, len(s.jobs))
	for _, job := range s.jobs {
		details = append(details, JobDetail{
			Name:      job.Name,
			Schedule:  job.Schedule,
			Prompt:    job.Prompt,
			Enabled:   job.Enabled,
			Agent:     job.Agent,
			Framework: job.Framework,
			Model:     job.Model,
		})
	}
	return details
}

// JobDetail is an exported view of a cron job.
type JobDetail struct {
	Name      string
	Schedule  string
	Prompt    string
	Enabled   bool
	Agent     string
	Framework string
	Model     string
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

		who := ""
		if job.Agent != "" {
			who = " @" + job.Agent
		}
		model := job.Framework
		if job.Model != "" {
			model = job.Model
		}
		if model != "" {
			who += " `" + model + "`"
		}
		lines = append(lines, fmt.Sprintf("%s *%s*%s — `%s`%s\n   %s", icon, job.Name, who, job.Schedule, nextStr, prompt))
	}

	return strings.Join(lines, "\n")
}
