package bot

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/you/claude-gateway/internal/agent"
	"github.com/you/claude-gateway/internal/skills"
)

const (
	defaultAgentKey     = "default"
	defaultAgentDisplay = "default"
	orchestratorSlot    = "orchestrator"
)

// agentSlot is one concurrent conversation in a Telegram chat.
type agentSlot struct {
	Key         string // normalized: "seoagent"
	Display     string // as addressed: "SEOagent"
	DNAName     string // matched DNA file name, if any
	DNA         string
	Dir         string
	ProjectPath string // owned repo; sidecar cwd when set
	Framework   string // DNA pin; empty = inherit gateway default
	Skills      []skills.Skill
}

func (s agentSlot) workDir(defaultDir string) string {
	p := strings.TrimRight(strings.TrimSpace(s.ProjectPath), "/")
	if p == "" {
		return defaultDir
	}
	return p
}

func sessionIDFor(chatID int64, key string) string {
	if key == "" || key == defaultAgentKey {
		return fmt.Sprintf("tg-%d", chatID)
	}
	return fmt.Sprintf("tg-%d-ag-%s", chatID, key)
}

func reqKey(chatID int64, slot string) string {
	if slot == "" {
		slot = defaultAgentKey
	}
	return fmt.Sprintf("%d:%s", chatID, slot)
}

func normalizeHandle(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func prettyHandle(name string) string {
	parts := strings.FieldsFunc(name, func(r rune) bool {
		return r == '-' || r == '_' || r == ' ' || r == '.'
	})
	var b strings.Builder
	for _, p := range parts {
		if p == "" {
			continue
		}
		rs := []rune(strings.ToLower(p))
		rs[0] = unicode.ToUpper(rs[0])
		b.WriteString(string(rs))
	}
	if b.Len() == 0 {
		return "Agent"
	}
	return b.String()
}

type parsedAgentCall struct {
	Mentioned bool
	Handle    string
	Rest      string
}

// parseAgentCall extracts a leading @Handle from a Telegram message.
// "@SEOagent do this" → handle SEOagent, rest "do this".
// "@SEOagent" alone is a switch with empty rest.
func parseAgentCall(text string) parsedAgentCall {
	t := strings.TrimSpace(text)
	if !strings.HasPrefix(t, "@") {
		return parsedAgentCall{}
	}
	runes := []rune(t)
	if len(runes) < 2 || !unicode.IsLetter(runes[1]) {
		return parsedAgentCall{}
	}
	j := 2
	for j < len(runes) {
		r := runes[j]
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' {
			j++
			continue
		}
		break
	}
	handle := string(runes[1:j])
	if len(handle) < 2 {
		return parsedAgentCall{}
	}
	rest := strings.TrimSpace(string(runes[j:]))
	rest = strings.TrimPrefix(rest, ":")
	rest = strings.TrimSpace(rest)
	return parsedAgentCall{Mentioned: true, Handle: handle, Rest: rest}
}

func formatAgentReply(display, body string, notification bool) string {
	body = strings.TrimSpace(body)
	if display == "" {
		display = defaultAgentDisplay
	}
	if notification {
		if lead := notificationLead(body); lead != "" {
			return fmt.Sprintf("Notification from @%s: %s\n\n%s", display, lead, body)
		}
		return fmt.Sprintf("Notification from @%s:\n\n%s", display, body)
	}
	return fmt.Sprintf("@%s\n\n%s", display, body)
}

func notificationLead(body string) string {
	t := strings.ToLower(body)
	hasQ := strings.Contains(body, "?") ||
		strings.Contains(t, "need you") ||
		strings.Contains(t, "need input") ||
		strings.Contains(t, "question")
	done := strings.Contains(t, "task complete") ||
		strings.Contains(t, "finished") ||
		strings.Contains(t, "i'm done") ||
		strings.Contains(t, "i am done") ||
		strings.HasPrefix(t, "done") ||
		strings.Contains(t, "complete")
	switch {
	case done && hasQ:
		return "Task complete, I need you to answer some questions."
	case hasQ:
		return "I need you to answer some questions."
	case done:
		return "Task complete."
	default:
		return ""
	}
}

const maxNamedDNAChars = 900

func namedAgentSystemPrompt(slot agentSlot) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are @%s — the same working style as @default, with this project's cwd and domain.\n", slot.Display)
	b.WriteString("Do exactly what @default would: Read/Edit/Write/Bash on the files, small diffs, then answer in chat.\n")
	b.WriteString("Never use Task, Agent, background compiles, or subagents. Never go env, go version, GOPROXY, GOMODCACHE, ls vendor, or pgrep. Do not run go build/vet unless Steve asked or you just hit a compile error you must show.\n")
	if p := strings.TrimRight(strings.TrimSpace(slot.ProjectPath), "/"); p != "" {
		fmt.Fprintf(&b, "WORKING DIRECTORY: %s\n", p)
		b.WriteString("Already there. Do not cd. Do not glob /workspace. Relative paths are from this cwd.\n")
	}
	b.WriteString("Conversation first: greeting or \"let's work on X\" without a file/bug/edit → plain text, no git tour.\n")
	b.WriteString("Prefer 1–3 tool calls. Do not prefix replies with @.\n")
	if slot.Dir != "" {
		fmt.Fprintf(&b, "Agent folder (only if you need a skill file): %s\n", slot.Dir)
	}
	if slot.DNA != "" {
		dna := slot.DNA
		if len(dna) > maxNamedDNAChars {
			dna = dna[:maxNamedDNAChars] + "\n\n[truncated]"
		}
		b.WriteString("\nDomain notes:\n\n")
		b.WriteString(dna)
	}
	return b.String()
}

type agentMatch struct {
	Name        string
	Key         string
	DNA         string
	Dir         string
	ProjectPath string
	Framework   string
	Skills      []skills.Skill
}

func matchAgentDNA(mention string, catalog []agentMatch) (hit *agentMatch, ambiguous []agentMatch) {
	key := normalizeHandle(mention)
	if key == "" {
		return nil, nil
	}
	keyTrim := strings.TrimSuffix(key, "agent")
	var exact, fuzzy []agentMatch
	for i := range catalog {
		nk := catalog[i].Key
		if nk == "" {
			nk = normalizeHandle(catalog[i].Name)
		}
		nkTrim := strings.TrimSuffix(nk, "agent")
		if nk == key || nkTrim == key || nk == keyTrim || nkTrim == keyTrim {
			exact = append(exact, catalog[i])
			continue
		}
		if len(keyTrim) >= 4 && (strings.Contains(nk, keyTrim) || strings.Contains(nkTrim, keyTrim)) {
			fuzzy = append(fuzzy, catalog[i])
		}
	}
	if len(exact) == 1 {
		h := exact[0]
		return &h, nil
	}
	if len(exact) > 1 {
		if pref := preferFolderAgent(exact); pref != nil {
			return pref, nil
		}
		return nil, exact
	}
	if len(fuzzy) == 1 {
		h := fuzzy[0]
		return &h, nil
	}
	if len(fuzzy) > 1 {
		if pref := preferFolderAgent(fuzzy); pref != nil {
			return pref, nil
		}
		return nil, fuzzy
	}
	return nil, nil
}

func preferFolderAgent(hits []agentMatch) *agentMatch {
	var folders []agentMatch
	for _, h := range hits {
		if h.Dir != "" {
			folders = append(folders, h)
		}
	}
	if len(folders) != 1 {
		return nil
	}
	h := folders[0]
	return &h
}

func (b *Bot) getFocus(chatID int64) agentSlot {
	b.currentAgentMu.RLock()
	defer b.currentAgentMu.RUnlock()
	s, ok := b.currentAgent[chatID]
	if !ok || s.Key == "" {
		return agentSlot{Key: defaultAgentKey, Display: defaultAgentDisplay}
	}
	return s
}

func (b *Bot) setFocus(chatID int64, slot agentSlot) {
	b.currentAgentMu.Lock()
	if b.currentAgent == nil {
		b.currentAgent = make(map[int64]agentSlot)
	}
	b.currentAgent[chatID] = slot
	b.currentAgentMu.Unlock()
}

func (b *Bot) isFocused(chatID int64, key string) bool {
	return b.getFocus(chatID).Key == key
}

func (b *Bot) catalogMatches() []agentMatch {
	if b.skillsManager == nil {
		return nil
	}
	chat := b.skillsManager.GetChatAgents()
	out := make([]agentMatch, 0, len(chat)+8)
	seen := map[string]bool{}
	for _, a := range chat {
		nk := normalizeHandle(a.Handle)
		seen[nk] = true
		out = append(out, agentMatch{
			Name:        a.Handle,
			Key:         nk,
			DNA:         a.DNA,
			Dir:         a.Dir,
			ProjectPath: a.ProjectPath,
			Framework:   a.Framework,
			Skills:      a.Skills,
		})
	}
	for _, a := range b.skillsManager.GetAgents() {
		nk := normalizeHandle(a.Name)
		if seen[nk] {
			continue
		}
		out = append(out, agentMatch{
			Name:        a.Name,
			Key:         nk,
			DNA:         a.Content,
			ProjectPath: skills.ExtractProjectPath(a.Content, a.Name),
			Framework:   skills.ExtractFramework(a.Content),
		})
	}
	return out
}

func isMainAssistantHandle(handle string) bool {
	k := normalizeHandle(handle)
	switch k {
	case defaultAgentKey, "home", "main", "assistant", "orchestrator":
		return true
	default:
		return false
	}
}

func (b *Bot) resolveSlot(handle string) agentSlot {
	slot := agentSlot{
		Key:     normalizeHandle(handle),
		Display: handle,
	}
	if slot.Key == "" || isMainAssistantHandle(handle) {
		slot.Key = defaultAgentKey
		slot.Display = defaultAgentDisplay
		return slot
	}
	if b.skillsManager != nil {
		if ag, ok := b.skillsManager.FindChatAgent(handle); ok {
			slot.DNAName = ag.Handle
			slot.DNA = ag.DNA
			slot.Dir = ag.Dir
			slot.ProjectPath = ag.ProjectPath
			slot.Framework = ag.Framework
			slot.Skills = ag.Skills
			return slot
		}
	}
	hit, amb := matchAgentDNA(handle, b.catalogMatches())
	if hit != nil && len(amb) == 0 {
		slot.DNAName = hit.Name
		slot.DNA = hit.DNA
		slot.Dir = hit.Dir
		slot.ProjectPath = hit.ProjectPath
		slot.Framework = hit.Framework
		slot.Skills = hit.Skills
		if slot.ProjectPath == "" && slot.DNA != "" {
			slot.ProjectPath = skills.ExtractProjectPath(slot.DNA, slot.DNAName)
		}
		if slot.Framework == "" && slot.DNA != "" {
			slot.Framework = skills.ExtractFramework(slot.DNA)
		}
		if slot.Display == "" {
			slot.Display = prettyHandle(hit.Name)
		}
	}
	return slot
}

func (b *Bot) switchMessage(chatID int64, slot agentSlot) string {
	var bld strings.Builder
	fmt.Fprintf(&bld, "Now chatting with @%s.", slot.Display)
	fw := b.frameworkFor(chatID, slot)
	fmt.Fprintf(&bld, "\nModel: %s (`%s`)", frameworkLabel(fw), b.modelForFramework(fw))
	if ov := b.slotFrameworkOverride(chatID, slot.Key); ov != "" {
		bld.WriteString(" — this chat only")
	} else if slot.Framework != "" {
		bld.WriteString(" — pinned in agent YAML")
	} else {
		bld.WriteString(" — gateway default")
	}
	if slot.DNAName != "" {
		fmt.Fprintf(&bld, "\nBound to `%s`.", slot.DNAName)
	} else if slot.Key != defaultAgentKey {
		bld.WriteString("\nNo unique agent folder matched — named thread with no private skills.")
	}
	if slot.ProjectPath != "" {
		fmt.Fprintf(&bld, "\nProject: `%s` (cwd — will not search for it).", slot.ProjectPath)
	}
	if n := len(slot.Skills); n > 0 {
		fmt.Fprintf(&bld, "\nPrivate skills: %d (plus global emails).", n)
	}
	if slot.Key == defaultAgentKey {
		bld.WriteString("\nThis is the main assistant (not a specialist). Messages without @ stay here.")
	} else {
		bld.WriteString("\nMessages without @ go to this agent. @AnotherName switches. /default or @default returns to the main assistant.")
	}
	bld.WriteString("\n/models or /framework here change this agent only.")
	return bld.String()
}

func frameworkLabel(fw string) string {
	if m, ok := lookupSelectable(fw); ok && (m.Key == fw || m.Model == fw || strings.EqualFold(m.Label, fw)) {
		return m.Label
	}
	for _, m := range selectableModels {
		if m.Key == fw {
			return m.Label
		}
	}
	switch fw {
	case "claude":
		return "Claude"
	case "opencode":
		return "OpenCode"
	case "":
		return "default"
	default:
		return fw
	}
}

func (b *Bot) slotFrameworkOverride(chatID int64, slotKey string) string {
	b.slotFWMu.RLock()
	defer b.slotFWMu.RUnlock()
	if b.slotFW == nil {
		return ""
	}
	return b.slotFW[reqKey(chatID, slotKey)]
}

func (b *Bot) setSlotFramework(chatID int64, slotKey, fw string) {
	key := reqKey(chatID, slotKey)
	b.slotFWMu.Lock()
	defer b.slotFWMu.Unlock()
	if b.slotFW == nil {
		b.slotFW = make(map[string]string)
	}
	if fw == "" {
		delete(b.slotFW, key)
		return
	}
	b.slotFW[key] = fw
}

func (b *Bot) frameworkFor(chatID int64, turn agentSlot) string {
	if ov := b.slotFrameworkOverride(chatID, turn.Key); ov != "" {
		return ov
	}
	if turn.Framework != "" {
		return turn.Framework
	}
	if sw, ok := b.agent.(*agent.Switcher); ok {
		if a := sw.Active(); a != "" {
			return a
		}
	}
	return "qwen"
}

func (b *Bot) backendFor(chatID int64, turn agentSlot) (agent.Backend, string) {
	fw := b.frameworkFor(chatID, turn)
	prov := providerOf(fw)
	if sw, ok := b.agent.(*agent.Switcher); ok {
		if be := sw.Get(prov); be != nil {
			return be, fw
		}
		return sw, sw.Active()
	}
	return b.agent, fw
}

func isLocalQwen(fw string) bool {
	switch providerOf(fw) {
	case "qwen", "ollama", "vllm":
		return true
	default:
		return false
	}
}

func looksLikeProgressPeek(s string) bool {
	t := strings.ToLower(strings.TrimSpace(s))
	if t == "" || t == "?" {
		return true
	}
	var b strings.Builder
	for _, r := range t {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == ' ' || r == '\'' {
			b.WriteRune(r)
		}
	}
	t = strings.Join(strings.Fields(b.String()), " ")
	if t == "" || len(t) > 90 {
		return false
	}
	switch t {
	case "hi", "hello", "hey", "yo", "ping",
		"progress", "status", "update",
		"any update", "any news",
		"still working", "still going",
		"where are we", "you there", "are you there",
		"catch me up":
		return true
	}
	needles := []string{
		"how's it going", "hows it going", "how is it going",
		"how's progress", "hows progress",
		"progress update", "status update",
		"what's happening", "whats happening",
		"what are you doing", "whatre you doing",
		"what's the progress", "whats the progress",
		"give me an update", "any update",
	}
	for _, n := range needles {
		if t == n || strings.HasPrefix(t, n+" ") {
			return true
		}
	}
	return false
}

func formatIdleStatus(display string) string {
	if display == "" {
		display = defaultAgentDisplay
	}
	return fmt.Sprintf("@%s is idle — nothing is running. A new message starts a task.", display)
}

func progressAllNote(focusDisplay string, focusWorking bool) string {
	if focusDisplay == "" {
		focusDisplay = defaultAgentDisplay
	}
	var b strings.Builder
	if !focusWorking {
		fmt.Fprintf(&b, "\n@%s (this chat) is idle — not listed above.\n", focusDisplay)
	}
	b.WriteString("\nOnly in-flight agents are listed. Idle specialists are omitted.\n")
	return b.String()
}

func formatRunReport(slot agentSlot, snap runSnapshot, peek bool) string {
	display := snap.Display
	if display == "" {
		display = slot.Display
	}
	if display == "" {
		display = defaultAgentDisplay
	}
	elapsed := time.Since(snap.Started).Truncate(time.Second)
	if elapsed < time.Second {
		elapsed = time.Second
	}
	var b strings.Builder
	if peek {
		fmt.Fprintf(&b, "@%s is still working — %s, %d actions. The task was not interrupted.\n", display, elapsed, snap.Actions)
	} else {
		fmt.Fprintf(&b, "@%s is already working — %s, %d actions. Your new message was not sent (that would cancel the task).\n", display, elapsed, snap.Actions)
		fmt.Fprintf(&b, "Wait for the notification, or /stop @%s to cancel.\n", display)
	}
	if g := strings.TrimSpace(snap.GPU); g != "" {
		fmt.Fprintf(&b, "%s\n", g)
	}
	if n := len(snap.Lines); n > 0 {
		b.WriteString("\nLast actions:\n")
		lines := snap.Lines
		if len(lines) > 12 {
			lines = lines[len(lines)-12:]
		}
		for _, ln := range lines {
			b.WriteString(ln)
			b.WriteByte('\n')
		}
	}
	if p := strings.TrimSpace(snap.Preview); p != "" {
		if len(p) > 800 {
			p = "..." + p[len(p)-800:]
		}
		b.WriteString("\nLive text:\n")
		b.WriteString(p)
		b.WriteByte('\n')
	}
	b.WriteString("\nPeek again with @")
	b.WriteString(display)
	b.WriteString(" or /progress. Cancel with /stop.")
	return b.String()
}

func applyGPUProgress(current, tool, input string) string {
	in := strings.TrimSpace(input)
	if strings.EqualFold(tool, "model") {
		low := strings.ToLower(in)
		if strings.HasPrefix(in, "GPU:") || strings.Contains(low, "queued") {
			return in
		}
		if in == "starting" {
			if current == "" || strings.Contains(strings.ToLower(current), "queued") {
				return "GPU: starting"
			}
			return current
		}
		if in == "first token" {
			cur := strings.ToLower(current)
			if current == "" || strings.Contains(cur, "queued") || strings.Contains(cur, "starting") {
				return "GPU: generating"
			}
		}
		return current
	}
	if current == "" {
		return current
	}
	low := strings.ToLower(current)
	if strings.Contains(low, "generating") || strings.Contains(low, "starting") {
		return "tools (not on GPU)"
	}
	return current
}

func gpuQueueRank(gpu string) int {
	g := strings.ToLower(gpu)
	if strings.Contains(g, "generating") || strings.Contains(g, "starting") {
		return 1
	}
	if i := strings.Index(g, "queued "); i >= 0 {
		rest := g[i+len("queued "):]
		var pos int
		if _, err := fmt.Sscanf(rest, "%d", &pos); err == nil && pos > 0 {
			return pos
		}
		return 50
	}
	return 100
}

func (b *Bot) slotSnapshot(chatID int64, slot string) (runSnapshot, bool) {
	key := reqKey(chatID, slot)
	b.activeReqsMu.Lock()
	e := b.activeReqs[key]
	b.activeReqsMu.Unlock()
	return e.copySnap()
}

func (b *Bot) tryBeginRun(key string, cancel context.CancelFunc) (entry *reqCancel, started bool, unbind func()) {
	id := b.activeReqSeq.Add(1)
	entry = &reqCancel{id: id, cancel: cancel}
	b.activeReqsMu.Lock()
	if prev := b.activeReqs[key]; prev != nil {
		b.activeReqsMu.Unlock()
		return prev, false, func() {}
	}
	b.activeReqs[key] = entry
	b.activeReqsMu.Unlock()
	return entry, true, func() {
		cancel()
		b.activeReqsMu.Lock()
		if cur := b.activeReqs[key]; cur != nil && cur.id == id {
			delete(b.activeReqs, key)
		}
		b.activeReqsMu.Unlock()
	}
}

func (b *Bot) runningSlots(chatID int64) []string {
	prefix := fmt.Sprintf("%d:", chatID)
	b.activeReqsMu.Lock()
	defer b.activeReqsMu.Unlock()
	var out []string
	for k := range b.activeReqs {
		if strings.HasPrefix(k, prefix) {
			out = append(out, strings.TrimPrefix(k, prefix))
		}
	}
	return out
}

func (b *Bot) replaceCancel(key string, cancel context.CancelFunc) func() {
	id := b.activeReqSeq.Add(1)
	entry := &reqCancel{id: id, cancel: cancel}
	b.activeReqsMu.Lock()
	if prev := b.activeReqs[key]; prev != nil && prev.cancel != nil {
		prev.cancel()
	}
	b.activeReqs[key] = entry
	b.activeReqsMu.Unlock()
	return func() {
		cancel()
		b.activeReqsMu.Lock()
		if cur := b.activeReqs[key]; cur != nil && cur.id == id {
			delete(b.activeReqs, key)
		}
		b.activeReqsMu.Unlock()
	}
}

func (b *Bot) cancelSlot(chatID int64, slot string) bool {
	key := reqKey(chatID, slot)
	b.activeReqsMu.Lock()
	entry := b.activeReqs[key]
	if entry != nil {
		delete(b.activeReqs, key)
	}
	b.activeReqsMu.Unlock()
	if entry == nil || entry.cancel == nil {
		return false
	}
	entry.cancel()
	return true
}

func (b *Bot) cancelAllSlots(chatID int64) int {
	prefix := fmt.Sprintf("%d:", chatID)
	b.activeReqsMu.Lock()
	var entries []*reqCancel
	for k, e := range b.activeReqs {
		if strings.HasPrefix(k, prefix) {
			entries = append(entries, e)
			delete(b.activeReqs, k)
		}
	}
	b.activeReqsMu.Unlock()
	n := 0
	for _, e := range entries {
		if e != nil && e.cancel != nil {
			e.cancel()
			n++
		}
	}
	return n
}
