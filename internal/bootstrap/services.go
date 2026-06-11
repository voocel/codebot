package bootstrap

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/voocel/codebot/internal/approval"
	"github.com/voocel/codebot/internal/config"
	mcpclient "github.com/voocel/codebot/internal/mcp"
	"github.com/voocel/codebot/internal/plugin"
	"github.com/voocel/codebot/internal/skill"
	"github.com/voocel/codebot/internal/storage"
)

type bootServices struct {
	approvalEngine *approval.Engine
	pluginCatalog  *plugin.Catalog
	skillCatalog   *skill.Catalog
	skills         []skill.Spec
	skillUsage     *skill.UsageTracker
	mcpManager     *mcpclient.Manager
	mcpServers     map[string]mcpclient.ServerConfig
	taskStore      *storage.TaskStore
	rosterStore    *storage.RosterStore
	transcripts    *storage.TranscriptStore
}

func buildServices(input *resolvedInput) (*bootServices, error) {
	pluginCatalog, err := plugin.LoadAll(input.cwd)
	if err != nil {
		return nil, fmt.Errorf("plugins: %w", err)
	}
	contrib := pluginCatalog.Contributions()

	skillCatalog := newSkillCatalog(input.cwd, contrib)
	skillUsage, err := resolveSkillUsageTracker()
	if err != nil {
		return nil, fmt.Errorf("skill usage: %w", err)
	}

	approvalEngine, err := newApprovalEngine(input)
	if err != nil {
		return nil, err
	}

	mcpManager, mcpServers := buildMCPServices(input.cwd, contrib)

	rosterStore, transcripts := newTeamStores(input)

	return &bootServices{
		approvalEngine: approvalEngine,
		pluginCatalog:  pluginCatalog,
		skillCatalog:   skillCatalog,
		skills:         skillCatalog.List(),
		skillUsage:     skillUsage,
		mcpManager:     mcpManager,
		mcpServers:     mcpServers,
		taskStore:      newTaskStore(input),
		rosterStore:    rosterStore,
		transcripts:    transcripts,
	}, nil
}

// newTeamStores builds the per-session team persistence: the roster (rooted at
// the session's team dir) and teammate transcripts (a transcripts/ subdir).
// Both share config.TeamDir so a session's coordination state is reclaimed
// together with its task list. SetDir creates nothing until the first write,
// so sessions that never form a team leave no team dir behind.
func newTeamStores(input *resolvedInput) (*storage.RosterStore, *storage.TranscriptStore) {
	teamDir := config.TeamDir(input.sessionStore.Header().SessionID)
	rosterStore := storage.NewRosterStore()
	if err := rosterStore.SetDir(teamDir); err != nil {
		fmt.Fprintf(os.Stderr, "warning: roster persistence: %v\n", err)
	}
	transcripts := storage.NewTranscriptStore(filepath.Join(teamDir, "transcripts"))
	return rosterStore, transcripts
}

func newSkillCatalog(cwd string, contrib plugin.Contributions) *skill.Catalog {
	extraSkillDirs := make([]skill.DirSource, 0, len(contrib.SkillDirs))
	for _, dir := range contrib.SkillDirs {
		extraSkillDirs = append(extraSkillDirs, skill.DirSource{
			Path:   dir.Path,
			Source: dir.Source,
		})
	}
	return skill.NewCatalog(cwd, contrib.SkillSpecs, extraSkillDirs...)
}

func newApprovalEngine(input *resolvedInput) (*approval.Engine, error) {
	rules, err := approval.ParseRuleSet(input.settings.Permissions.Allow, input.settings.Permissions.Deny)
	if err != nil {
		return nil, fmt.Errorf("parse permission rules: %w", err)
	}

	approvalEngine, err := approval.NewEngine(input.cwd, input.approvalMode, rules, approvalAuditor(config.AuditLogPath()))
	if err != nil {
		return nil, fmt.Errorf("approval engine: %w", err)
	}
	memoryDir := config.MemoryDir(input.cwd)
	plansDir := config.PlansDir(input.cwd)
	approvalEngine.SetFilesystemRoots(approval.FilesystemRoots{
		ReadRoots:  append(input.settings.Permissions.ReadRoots, config.SessionsDir(input.cwd)),
		WriteRoots: input.settings.Permissions.WriteRoots,
		// Auto-memory + plan files live outside the user's workspace; mark
		// them as harness-managed paths so reads/writes skip the OutsideRoots
		// prompt and balanced-mode write approval. The same declaration also
		// drives plan-mode write permission: the agentcore engine treats any
		// InternalWritable path as harness-trusted during plan mode, so the
		// model can update plan files and the auto-memory store while
		// pair-planning without per-path hooks.
		InternalReadable: []string{memoryDir, plansDir},
		InternalWritable: []string{memoryDir, plansDir},
	})
	return approvalEngine, nil
}

func buildMCPServices(cwd string, contrib plugin.Contributions) (*mcpclient.Manager, map[string]mcpclient.ServerConfig) {
	mcpServers := mcpclient.LoadAllMCPServers(cwd)
	for name, cfg := range contrib.MCPServers {
		if mcpServers == nil {
			mcpServers = make(map[string]mcpclient.ServerConfig)
		}
		if _, exists := mcpServers[name]; !exists {
			mcpServers[name] = cfg
		}
	}
	// Always create a manager — even with zero servers configured — so the
	// session's refresh hook and the UI's /mcp reload operate on one stable
	// instance for the process lifetime.
	return mcpclient.NewManager(), mcpServers
}

func newTaskStore(input *resolvedInput) *storage.TaskStore {
	taskStore := storage.NewTaskStore()
	taskDir := filepath.Join(config.TasksDir(), input.sessionStore.Header().SessionID)
	if err := taskStore.SetDir(taskDir); err != nil {
		fmt.Fprintf(os.Stderr, "warning: task persistence: %v\n", err)
	}
	return taskStore
}

func resolveSkillUsageTracker() (*skill.UsageTracker, error) {
	configDir := config.UserConfigDir()
	if configDir == "" {
		return nil, nil
	}
	return skill.NewUsageTracker(filepath.Join(configDir, "skill-usage.json"))
}

func skillUsageScores(tracker *skill.UsageTracker) map[string]float64 {
	if tracker == nil {
		return nil
	}
	return tracker.Scores(time.Now())
}

func approvalAuditor(path string) func(approval.AuditEntry) {
	var mu sync.Mutex
	return func(e approval.AuditEntry) {
		entry := map[string]any{
			"time":       e.Time.Format(time.RFC3339Nano),
			"mode":       string(e.Mode),
			"plan_mode":  e.PlanMode,
			"tool":       e.Tool,
			"capability": string(e.Capability),
			"summary":    e.Summary,
			"decision":   e.Decision,
			"allow":      e.Allow,
		}
		if e.Reason != "" {
			entry["reason"] = e.Reason
		}
		data, err := json.Marshal(entry)
		if err != nil {
			return
		}
		data = append(data, '\n')

		mu.Lock()
		defer mu.Unlock()
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return
		}
		defer f.Close()
		_, _ = f.Write(data)
	}
}
