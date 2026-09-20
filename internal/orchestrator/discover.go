package orchestrator

import (
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

var skipTeamFiles = map[string]bool{
	"readme.md":             true,
	"consensus-protocol.md": true,
}

// RegisterDiscoveredTeams scans DNA roots for team folders and agent
// markdown files, then merges them into the in-memory team list.
// Existing hardcoded teams keep their names and metadata; new folders
// become new teams. Safe to call at process start (and on a watcher).
func RegisterDiscoveredTeams(roots []string) int {
	found := discoverTeams(roots)
	teamsMu.Lock()
	defer teamsMu.Unlock()
	added := 0
	for _, t := range found {
		n := mergeTeamLocked(t)
		added += n
	}
	if added > 0 {
		log.Printf("Orchestrator: registered %d agent(s) from disk across %d discovered folder(s)", added, len(found))
	}
	return added
}

func discoverTeams(roots []string) []Team {
	var out []Team
	seenDir := map[string]bool{}
	for _, root := range roots {
		abs, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		if _, err := os.Stat(abs); err != nil {
			continue
		}
		_ = filepath.Walk(abs, func(path string, info os.FileInfo, err error) error {
			if err != nil || info == nil || !info.IsDir() {
				return nil
			}
			if path == abs {
				return nil
			}
			if seenDir[path] {
				return nil
			}
			seenDir[path] = true
			files := agentMarkdownInDir(path)
			hasChildTeams := dirHasTeamSubdirs(path)
			if len(files) == 0 {
				return nil
			}
			type pair struct{ abs, rel string }
			var pairs []pair
			for _, f := range files {
				rel, err := filepath.Rel(abs, f)
				if err != nil {
					continue
				}
				pairs = append(pairs, pair{abs: f, rel: filepath.ToSlash(rel)})
			}
			if len(pairs) == 0 {
				return nil
			}
			if hasChildTeams {
				for _, p := range pairs {
					out = append(out, Team{Name: teamNameFromFile(p.rel), Agents: []Agent{agentFromFile(p.abs, p.rel)}})
				}
				return nil
			}
			ags := make([]Agent, 0, len(pairs))
			for _, p := range pairs {
				ags = append(ags, agentFromFile(p.abs, p.rel))
			}
			out = append(out, Team{Name: filepath.Base(path), Agents: ags})
			return nil
		})
	}
	return out
}

func agentMarkdownInDir(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		lower := strings.ToLower(name)
		if !strings.HasSuffix(lower, ".md") || skipTeamFiles[lower] {
			continue
		}
		if strings.HasPrefix(lower, "skill-") {
			continue
		}
		files = append(files, filepath.Join(dir, name))
	}
	return files
}

func dirHasTeamSubdirs(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() && len(agentMarkdownInDir(filepath.Join(dir, e.Name()))) > 0 {
			return true
		}
	}
	return false
}

func teamNameFromFile(rel string) string {
	base := strings.TrimSuffix(filepath.Base(rel), filepath.Ext(rel))
	base = strings.TrimSuffix(base, "-agent")
	return strings.ToLower(base)
}

func agentFromFile(absPath, rel string) Agent {
	ag := Agent{
		Name:    prettyAgentName(rel),
		DNAFile: rel,
		Tier:    4,
		Weight:  1,
	}
	lower := strings.ToLower(filepath.Base(rel))
	if strings.Contains(lower, "security") {
		ag.Tier = 1
		ag.Weight = 1000
		ag.HasVetoPower = true
	} else if strings.Contains(lower, "hacker") {
		ag.Tier = 2
		ag.Weight = 100
	}
	if data, err := os.ReadFile(absPath); err == nil {
		applyDNAFrontmatter(&ag, string(data))
	}
	return ag
}

func prettyAgentName(rel string) string {
	base := strings.TrimSuffix(filepath.Base(rel), filepath.Ext(rel))
	base = strings.TrimSuffix(base, "-agent")
	parts := strings.FieldsFunc(base, func(r rune) bool { return r == '-' || r == '_' })
	var b strings.Builder
	for _, p := range parts {
		if p == "" {
			continue
		}
		p = strings.ToLower(p)
		b.WriteString(strings.ToUpper(p[:1]))
		if len(p) > 1 {
			b.WriteString(p[1:])
		}
	}
	if b.Len() == 0 {
		return "Agent"
	}
	return b.String()
}

func applyDNAFrontmatter(ag *Agent, content string) {
	trimmed := strings.TrimSpace(content)
	if !strings.HasPrefix(trimmed, "---") {
		return
	}
	rest := trimmed[3:]
	end := strings.Index(rest, "---")
	if end <= 0 {
		return
	}
	for _, line := range strings.Split(rest[:end], "\n") {
		line = strings.TrimSpace(line)
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		val = strings.Trim(strings.TrimSpace(val), `"'`)
		switch key {
		case "name":
			if val != "" {
				ag.Name = val
			}
		case "tier":
			if n, err := strconv.Atoi(val); err == nil && n > 0 {
				ag.Tier = n
			}
		case "weight":
			if n, err := strconv.Atoi(val); err == nil && n > 0 {
				ag.Weight = n
			}
		case "veto", "has_veto", "veto_power":
			ag.HasVetoPower = val == "true" || val == "yes" || val == "1"
		}
	}
}

func mergeTeamLocked(t Team) int {
	if t.Name == "" || len(t.Agents) == 0 {
		return 0
	}
	claimed := map[string]string{}
	for name, team := range allTeams {
		for _, a := range team.Agents {
			claimed[filepath.ToSlash(a.DNAFile)] = name
		}
	}
	var unclaimed []Agent
	for _, a := range t.Agents {
		key := filepath.ToSlash(a.DNAFile)
		if _, ok := claimed[key]; ok {
			continue
		}
		unclaimed = append(unclaimed, a)
	}
	if len(unclaimed) == 0 {
		return 0
	}
	target := t.Name
	if owner := folderOwnerLocked(t); owner != "" {
		target = owner
	}
	existing, ok := allTeams[target]
	if !ok {
		allTeams[target] = Team{Name: target, Agents: unclaimed}
		return len(unclaimed)
	}
	existing.Agents = append(existing.Agents, unclaimed...)
	allTeams[target] = existing
	return len(unclaimed)
}

// folderOwnerLocked maps a discovered folder onto a hardcoded team when
// that folder's files are already split across known teams (frontend-team
// → fe-dev-team, kotlin-team → mobile-review).
func folderOwnerLocked(t Team) string {
	counts := map[string]int{}
	for _, a := range t.Agents {
		for name, team := range allTeams {
			for _, ea := range team.Agents {
				if filepath.ToSlash(ea.DNAFile) == filepath.ToSlash(a.DNAFile) {
					counts[name]++
				}
			}
		}
	}
	best := ""
	bestN := 0
	for name, n := range counts {
		if n > bestN {
			best = name
			bestN = n
		}
	}
	if bestN == 0 {
		aliases := map[string]string{
			"frontend-team": "fe-dev-team",
			"kotlin-team":   "mobile-review",
		}
		if a, ok := aliases[t.Name]; ok {
			return a
		}
		return ""
	}
	return best
}
