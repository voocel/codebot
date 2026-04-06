package bootstrap

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/codebot/internal/agent"
	"github.com/voocel/codebot/internal/approval"
	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/hooks"
	mcpclient "github.com/voocel/codebot/internal/mcp"
	"github.com/voocel/codebot/internal/plugin"
	"github.com/voocel/codebot/internal/skill"
	"github.com/voocel/codebot/internal/storage"
	localtools "github.com/voocel/codebot/internal/tools"
)

// Options controls how runtime bootstraps.
type Options struct {
	Cwd string

	Continue   bool
	Resume     bool
	NonTTYMode bool

	ApprovalMode  string
	ToolFactories []ToolFactory
	ModelFactory  agent.ModelFactory
}

// Runtime is the bootstrapped app runtime state.
type Runtime struct {
	Cwd       string
	GitBranch string

	ApprovalEngine *approval.Engine
	TaskRuntime    *agentcore.TaskRuntime

	Settings            config.Resolved
	Session             *agent.Session
	SessionStore        *storage.Store
	PluginCatalog       *plugin.Catalog
	SkillCatalog        *skill.Catalog
	MCPManager          *mcpclient.Manager
	MCPServers          map[string]mcpclient.ServerConfig // for async connection in TUI
	HookRunner          *hooks.Runner                     // nil if no hooks configured
	EnvHint             string                            // non-empty when credentials come from environment variable
	PlanSlug            string                            // restored plan slug (empty if no plan)
	PlanTitle           string                            // restored plan title
	PlanPhase           string                            // restored plan phase
	PlanPreMode         string                            // restored plan pre-mode
	PlanAllowedCommands []storage.AllowedCommandEntry     // restored allowed command prefixes
}

// Close releases runtime resources.
func (r *Runtime) Close() {
	if r.HookRunner != nil {
		r.HookRunner.RunSessionEnd(context.Background())
	}
	if r.TaskRuntime != nil {
		r.TaskRuntime.StopAll()
	}
	if r.MCPManager != nil {
		r.MCPManager.Close()
	}
	if r.Session != nil {
		r.Session.Close()
	}
}

// Boot creates a ready-to-run runtime.
func Boot(opts Options) (*Runtime, error) {
	input, err := resolveBootInput(opts)
	if err != nil {
		return nil, err
	}

	closeStoreOnError := true
	defer func() {
		if closeStoreOnError && input.store != nil {
			_ = input.store.Close()
		}
	}()

	spec, err := assembleBootSpec(input, opts.ToolFactories)
	if err != nil {
		return nil, err
	}

	rt, err := buildRuntime(input, spec)
	if err != nil {
		return nil, err
	}

	closeStoreOnError = false
	go localtools.CleanOldOutputs()
	return rt, nil
}

func detectGitBranch(cwd string) string {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func resolveSession(mgr *storage.Manager, cwd string, cont, resume, nonTTY bool) (*storage.Store, error) {
	switch {
	case cont:
		info, err := mgr.MostRecent()
		if err != nil {
			return nil, err
		}
		if info == nil {
			return mgr.Create(cwd)
		}
		return mgr.OpenPath(info.Path)
	case resume:
		sessions, err := mgr.List()
		if err != nil {
			return nil, err
		}
		if len(sessions) == 0 {
			return mgr.Create(cwd)
		}
		if nonTTY {
			return nil, fmt.Errorf("-r requires interactive terminal, use -c or -session in non-interactive mode")
		}

		fmt.Fprintf(os.Stderr, "Available sessions:\n")
		for i, s := range sessions {
			name := s.ID
			if s.Name != "" {
				name = s.Name
			}
			fmt.Fprintf(os.Stderr, "  %d. %s  (%s)  %s  [id:%s]\n",
				i+1, name, s.Cwd, s.Updated.Format("2006-01-02 15:04"), s.ID)
		}
		fmt.Fprintf(os.Stderr, "Select session number or id: ")

		reader := bufio.NewReader(os.Stdin)
		raw, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("read session selection: %w", err)
		}
		choice := strings.TrimSpace(raw)
		if choice == "" {
			return nil, fmt.Errorf("no session selected")
		}

		for _, s := range sessions {
			if s.ID == choice {
				return mgr.OpenPath(s.Path)
			}
		}

		if idx, convErr := strconv.Atoi(choice); convErr == nil {
			if idx < 1 || idx > len(sessions) {
				return nil, fmt.Errorf("invalid session index %d (range: 1-%d)", idx, len(sessions))
			}
			return mgr.OpenPath(sessions[idx-1].Path)
		}
		return nil, fmt.Errorf("invalid session selection %q", choice)
	default:
		return mgr.Create(cwd)
	}
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
