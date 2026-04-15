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

	return &bootServices{
		approvalEngine: approvalEngine,
		pluginCatalog:  pluginCatalog,
		skillCatalog:   skillCatalog,
		skills:         skillCatalog.List(),
		skillUsage:     skillUsage,
		mcpManager:     mcpManager,
		mcpServers:     mcpServers,
		taskStore:      newTaskStore(input),
	}, nil
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
	approvalEngine.SetFilesystemRoots(approval.FilesystemRoots{
		ReadRoots:  append(input.settings.Permissions.ReadRoots, config.SessionsDir(input.cwd)),
		WriteRoots: input.settings.Permissions.WriteRoots,
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
	if len(mcpServers) == 0 {
		return nil, nil
	}
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
