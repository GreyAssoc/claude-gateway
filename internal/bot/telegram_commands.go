package bot

import (
	"fmt"
	"log"
	"strings"
	"unicode/utf8"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/you/claude-gateway/internal/skills"
)

const telegramMaxCommands = 100

func telegramSafeCommand(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == ' ':
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), "_")
	for strings.Contains(out, "__") {
		out = strings.ReplaceAll(out, "__", "_")
	}
	if out == "" {
		return ""
	}
	if out[0] >= '0' && out[0] <= '9' {
		out = "c_" + out
	}
	if len(out) > 32 {
		out = out[:32]
	}
	return out
}

func clipDesc(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "Command"
	}
	if utf8.RuneCountInString(s) <= 256 {
		return s
	}
	runes := []rune(s)
	return string(runes[:253]) + "..."
}

func coreTelegramCommands() []tgbotapi.BotCommand {
	return []tgbotapi.BotCommand{
		{Command: "start", Description: "Help and command overview"},
		{Command: "agents", Description: "List @agent names (tap to switch)"},
		{Command: "default", Description: "Leave specialist; chat with the main assistant"},
		{Command: "who", Description: "Which agent you are chatting with"},
		{Command: "chats", Description: "Same as /who"},
		{Command: "progress", Description: "Peek at a running agent without cancelling"},
		{Command: "stop", Description: "Stop current agent (@Name or all)"},
		{Command: "status", Description: "Gateway health and rate limits"},
		{Command: "models", Description: "Model for this agent only (Qwen/DeepSeek/Flash/Grok)"},
		{Command: "framework", Description: "Framework for this agent only"},
		{Command: "compact", Description: "Summarise session and start fresh"},
		{Command: "reset", Description: "Clear current agent session"},
		{Command: "skills", Description: "List global and per-agent skills"},
		{Command: "cost", Description: "Token usage this session"},
		{Command: "model", Description: "Show actual API model in use"},
		{Command: "googleauth", Description: "Google Workspace OAuth"},
		{Command: "grokauth", Description: "Sign in SuperGrok"},
		{Command: "gauntlet", Description: "Run full review gauntlet"},
		{Command: "reviewgo", Description: "Run go-dev-team review"},
		{Command: "reviewgosec", Description: "Run go-sec-team review"},
		{Command: "reviewfe", Description: "Run frontend dev review"},
		{Command: "reviewfesec", Description: "Run frontend security review"},
		{Command: "reviewdesign", Description: "Run design review team"},
		{Command: "reviewintegration", Description: "Run integration review"},
		{Command: "reviewpython", Description: "Run python-dev-team review"},
		{Command: "reviewpythonsec", Description: "Run python-sec-team review"},
		{Command: "reviewauto", Description: "Auto-detect language and review"},
		{Command: "reviewautosec", Description: "Auto-detect with security focus"},
		{Command: "cronlist", Description: "List scheduled jobs"},
		{Command: "cronadd", Description: "Add cron job"},
		{Command: "crondel", Description: "Remove a cron job"},
		{Command: "cronenable", Description: "Enable a cron job"},
		{Command: "crondisable", Description: "Disable a cron job"},
		{Command: "cronrun", Description: "Run a cron job now"},
		{Command: "journal", Description: "View daily journal"},
		{Command: "journalsearch", Description: "Search journal entries"},
		{Command: "today", Description: "Today's journal"},
		{Command: "memoryremember", Description: "Save a memory"},
		{Command: "memorylist", Description: "List memories by category"},
		{Command: "memoryforget", Description: "Remove a memory by text match"},
		{Command: "memorieslist", Description: "List curated memories"},
		{Command: "peoplelist", Description: "List contacts"},
		{Command: "personadd", Description: "Add a person"},
		{Command: "persondel", Description: "Remove a person"},
		{Command: "personfind", Description: "Find a person"},
		{Command: "contactadd", Description: "Add contact detail to person"},
		{Command: "profile", Description: "View user profile"},
		{Command: "profileset", Description: "Set profile aspect"},
		{Command: "profiledel", Description: "Delete profile aspect"},
		{Command: "context", Description: "Current context layers"},
		{Command: "bootstrapset", Description: "Add/update bootstrap instruction"},
		{Command: "dbcheck", Description: "Check PostgreSQL conversations"},
		{Command: "orchestratoragents", Description: "List orchestrator review agents"},
		{Command: "orchestratorruns", Description: "Recent orchestrator runs"},
		{Command: "skilldetail", Description: "Show one skill"},
		{Command: "skillenable", Description: "Enable a global skill"},
		{Command: "skilldisable", Description: "Disable a global skill"},
		{Command: "sync", Description: "Sync agents/commands Claude ↔ OpenCode"},
	}
}

func buildTelegramCommandList(sm *skills.Manager) []tgbotapi.BotCommand {
	seen := map[string]bool{}
	var out []tgbotapi.BotCommand
	add := func(cmd, desc string) {
		cmd = telegramSafeCommand(cmd)
		if cmd == "" || seen[cmd] {
			return
		}
		if len(out) >= telegramMaxCommands {
			return
		}
		seen[cmd] = true
		out = append(out, tgbotapi.BotCommand{Command: cmd, Description: clipDesc(desc)})
	}

	for _, c := range coreTelegramCommands() {
		add(c.Command, c.Description)
	}
	if sm != nil {
		for _, a := range sm.GetChatAgents() {
			add(a.Handle, fmt.Sprintf("@%s — chat with this agent", prettyHandle(a.Handle)))
		}
		for _, s := range sm.GetCommandSkills() {
			if s.Command == "" {
				continue
			}
			desc := s.Description
			if desc == "" {
				desc = s.Name
			}
			if s.Owner != "" {
				desc = fmt.Sprintf("@%s skill: %s", prettyHandle(s.Owner), desc)
			}
			add(s.Command, desc)
		}
	}
	return out
}

func (b *Bot) sendAgentPicker(chatID int64) {
	if b.skillsManager == nil {
		b.sendMessage(chatID, "No agents loaded.")
		return
	}
	agents := b.skillsManager.GetChatAgents()
	if len(agents) == 0 {
		b.sendMessage(chatID, "No folder agents under .claude/agents/ yet.")
		return
	}
	var sb strings.Builder
	sb.WriteString("Agents. Telegram cannot put these under @ (that picker is usernames). Tap one, or type @Name in a message.\n\n")
	var rows [][]tgbotapi.InlineKeyboardButton
	var row []tgbotapi.InlineKeyboardButton
	for _, a := range agents {
		label := "@" + prettyHandle(a.Handle)
		sb.WriteString(label)
		if n := len(a.Skills); n > 0 {
			fmt.Fprintf(&sb, " (%d skills)", n)
		}
		sb.WriteByte('\n')
		data := "agent:" + a.Handle
		if len(data) > 64 {
			continue
		}
		btnLabel := label
		if utf8.RuneCountInString(btnLabel) > 32 {
			btnLabel = string([]rune(btnLabel)[:32])
		}
		row = append(row, tgbotapi.NewInlineKeyboardButtonData(btnLabel, data))
		if len(row) == 2 {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	msg := tgbotapi.NewMessage(chatID, sb.String())
	if len(rows) > 0 {
		msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(rows...)
	}
	if _, err := b.api.Send(msg); err != nil {
		log.Printf("agent picker send failed: %v", err)
	}
}
