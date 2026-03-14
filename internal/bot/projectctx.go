package bot

import (
	"encoding/json"
	"log"
	"os"
	"strings"
	"time"
)

// projectRegistry holds the parsed project-registry.json data.
// It is loaded lazily and cached for 5 minutes.
type projectRegistry struct {
	Projects map[string]projectEntry `json:"projects"`
}

type projectEntry struct {
	Path    string            `json:"path"`
	Aliases []string          `json:"aliases"`
	Status  string            `json:"status"`
	Tech    string            `json:"tech"`
	Server  map[string]server `json:"server"`
	Git     *gitInfo          `json:"git"`
}

type server struct {
	IP         string `json:"ip"`
	URL        string `json:"url"`
	DeployPath string `json:"deploy_path"`
	SSHKey     string `json:"ssh_key"`
	User       string `json:"user"`
}

type gitInfo struct {
	Remote string `json:"remote"`
	Branch string `json:"branch"`
}

var (
	registryPath     string
	registryCache    *projectRegistry
	registryCacheAt  time.Time
	registryCacheTTL = 5 * time.Minute
)

func setRegistryPath(path string) {
	registryPath = path
}

func loadProjectRegistry() *projectRegistry {
	if registryPath == "" {
		return nil
	}
	now := time.Now()
	if registryCache != nil && now.Sub(registryCacheAt) < registryCacheTTL {
		return registryCache
	}
	data, err := os.ReadFile(registryPath)
	if err != nil {
		return registryCache // return stale cache on error
	}
	var reg projectRegistry
	if err := json.Unmarshal(data, &reg); err != nil {
		log.Printf("projectctx: failed to parse registry: %v", err)
		return registryCache
	}
	registryCache = &reg
	registryCacheAt = now
	return &reg
}

// resolveProject matches user text against project aliases.
// Returns the project name and entry, or empty string if no match.
// Prefers the longest alias match to avoid false positives.
func resolveProject(text string) (string, *projectEntry) {
	reg := loadProjectRegistry()
	if reg == nil {
		return "", nil
	}

	lower := strings.ToLower(text)
	var bestName string
	var bestEntry *projectEntry
	bestLen := 0

	for name, proj := range reg.Projects {
		p := proj // capture loop var
		if strings.Contains(lower, strings.ToLower(name)) && len(name) > bestLen {
			bestName = name
			bestEntry = &p
			bestLen = len(name)
		}
		for _, alias := range proj.Aliases {
			if strings.Contains(lower, strings.ToLower(alias)) && len(alias) > bestLen {
				bestName = name
				bestEntry = &p
				bestLen = len(alias)
			}
		}
	}
	return bestName, bestEntry
}

// setActiveProjectFile writes the active project name to the temp file
// that both Claude Code and OpenCode hooks read.
func setActiveProjectFile(projectName string) {
	// Write for both frameworks
	for _, path := range []string{"/tmp/claude-active-project", "/tmp/opencode-active-project"} {
		if err := os.WriteFile(path, []byte(projectName), 0644); err != nil {
			log.Printf("projectctx: failed to write %s: %v", path, err)
		}
	}
}

// formatProjectBrief returns a short context string for enriching Claude's prompt.
func formatProjectBrief(name string, proj *projectEntry) string {
	var parts []string
	parts = append(parts, "Active project: "+name)
	parts = append(parts, "Path: "+proj.Path)
	if proj.Tech != "" {
		parts = append(parts, "Tech: "+proj.Tech)
	}
	for env, srv := range proj.Server {
		line := "Server (" + env + "): " + srv.IP
		if srv.URL != "" {
			line += " — " + srv.URL
		}
		if srv.DeployPath != "" {
			line += " — deploy: " + srv.DeployPath
		}
		if srv.SSHKey != "" {
			line += " — key: " + srv.SSHKey
		}
		parts = append(parts, line)
	}
	if proj.Git != nil && proj.Git.Remote != "" {
		parts = append(parts, "Git: "+proj.Git.Remote)
	}
	return strings.Join(parts, "\n")
}
