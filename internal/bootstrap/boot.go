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
	"github.com/voocel/agentcore/memory"
	"github.com/voocel/codebot/internal/agent"
	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/policy"
	"github.com/voocel/codebot/internal/provider"
	"github.com/voocel/codebot/internal/storage"
	localtools "github.com/voocel/codebot/internal/tools"

	mcpclient "github.com/voocel/codebot/internal/mcp"
)

// Options controls how runtime bootstraps.
type Options struct {
	Cwd string

	Continue   bool
	Resume     bool
	Session    string
	NonTTYMode bool

	PolicyProfile string
	ToolFactories []ToolFactory
	ModelFactory  agent.ModelFactory
}

// Runtime is the bootstrapped app runtime state.
type Runtime struct {
	Cwd       string
	GitBranch string

	PolicyProfile policy.Profile

	Settings     config.Resolved
	Session      *agent.Session
	MCPManager   *mcpclient.Manager
}

// Close releases runtime resources.
func (r *Runtime) Close() {
	if r.MCPManager != nil {
		r.MCPManager.Close()
	}
	if r.Session != nil {
		r.Session.Close()
	}
}

// Boot creates a ready-to-run runtime.
func Boot(opts Options) (*Runtime, error) {
	cwd := opts.Cwd
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("get cwd: %w", err)
		}
	}

	settings := config.ResolveAll(cwd)

	registry := provider.NewModelRegistry()
	provider.StartPricingRefresh(registry, config.UserConfigDir())
	profile, err := parseProfile(opts.PolicyProfile)
	if err != nil {
		return nil, err
	}
	createModel := opts.ModelFactory
	if createModel == nil {
		createModel = provider.CreateModel
	}

	// Interactive setup when API key is missing for the active provider.
	apiKey, _ := settings.ProviderCredentials(settings.Provider)
	if apiKey == "" {
		if opts.NonTTYMode {
			return nil, fmt.Errorf("api key not set; set %s or configure providers in %s",
				config.ProviderEnvKey(settings.Provider), filepath.Join(config.UserConfigDir(), "settings.json"))
		}
		err := config.RunSetup(settings, func(prov string) []config.ModelOption {
			entries := registry.FindByProvider(prov)
			result := make([]config.ModelOption, len(entries))
			for i, e := range entries {
				result[i] = config.ModelOption{ID: e.ID, Name: e.Name}
			}
			return result
		})
		if err != nil {
			return nil, fmt.Errorf("setup: %w", err)
		}
		settings = config.ResolveAll(cwd)
	}

	manager := storage.NewManager(config.SessionsDir(cwd))
	store, err := resolveSession(manager, cwd, opts.Continue, opts.Resume, opts.Session, opts.NonTTYMode)
	if err != nil {
		return nil, fmt.Errorf("session: %w", err)
	}
	closeStoreOnError := true
	defer func() {
		if closeStoreOnError && store != nil {
			_ = store.Close()
		}
	}()

	var snapshot storage.ContextSnapshot
	if opts.Continue || opts.Resume || opts.Session != "" {
		snapshot, err = store.BuildSnapshot()
		if err != nil {
			return nil, fmt.Errorf("restore context: %w", err)
		}
	}

	activeProvider := settings.Provider
	if snapshot.Provider != "" {
		activeProvider = snapshot.Provider
	}
	activeModel := settings.Model
	if snapshot.Model != "" {
		activeModel = snapshot.Model
	}
	activeAPIKey, activeBaseURL := settings.ProviderCredentials(activeProvider)

	chatModel, err := createModel(activeProvider, activeModel, activeAPIKey, activeBaseURL)
	if err != nil {
		return nil, fmt.Errorf("create model: %w", err)
	}

	ctxFiles := config.LoadContextFiles(cwd)
	skills := config.LoadSkills(cwd)

	pol := policy.New(policy.Config{
		Profile:     profile,
		Workspace:   cwd,
		Interactive: !opts.NonTTYMode,
		OnAudit:     fileAuditor(config.AuditLogPath()),
	})

	builtTools := buildTools(cwd, opts.ToolFactories)
	askTool := localtools.NewAskUser()
	taskStore, taskTools := localtools.NewTaskTools()
	builtTools = append(builtTools,
		localtools.NewWebFetch(settings.SearchProvider, settings.SearchAPIKey),
		localtools.NewWebSearch(settings.SearchProvider, settings.SearchAPIKey),
		askTool,
	)
	builtTools = append(builtTools, taskTools...)

	// Apply output size limits to tools that may produce large output.
	builtTools = localtools.WrapWithOutputLimit(builtTools)

	// Enable task persistence scoped to this session.
	taskDir := filepath.Join(config.TasksDir(), store.Header().SessionID)
	if err := taskStore.SetDir(taskDir); err != nil {
		fmt.Fprintf(os.Stderr, "warning: task persistence: %v\n", err)
	}

	// SubAgent tool: delegate tasks to isolated sub-agents (explore, plan, coder).
	subagentTool := buildSubAgentTool(subAgentDeps{
		Cwd:         cwd,
		Model:       chatModel,
		AllTools:    builtTools,
		CreateModel: createModel,
		Provider:    activeProvider,
		Providers:   settings.Providers,
		SmallModel:  settings.SmallModel,
	})
	builtTools = append(builtTools, subagentTool)

	// Start MCP servers and collect their tools.
	var mcpManager *mcpclient.Manager
	mcpServers := mcpclient.LoadAllMCPServers(cwd)
	baseTools := builtTools // snapshot before MCP tools are appended (includes subagent)
	if len(mcpServers) > 0 {
		mcpManager = mcpclient.NewManager()
		if errs := mcpManager.StartAll(context.Background(), mcpServers); len(errs) > 0 {
			for _, e := range errs {
				fmt.Fprintf(os.Stderr, "mcp: %v\n", e)
			}
		}
		builtTools = append(builtTools, mcpManager.Tools(context.Background())...)
	}

	toolInfos := make([]config.ToolInfo, len(builtTools))
	for i, t := range builtTools {
		toolInfos[i] = config.ToolInfo{Name: t.Name(), Description: t.Description()}
	}

	systemPrompt := config.BuildSystemPrompt(cwd, ctxFiles, toolInfos, skills)

	// Append MCP server instructions to system prompt.
	if mcpManager != nil {
		if instructions := mcpManager.Instructions(); len(instructions) > 0 {
			var sb strings.Builder
			sb.WriteString(systemPrompt)
			for _, inst := range instructions {
				sb.WriteString("\n\n")
				sb.WriteString(inst)
			}
			systemPrompt = sb.String()
		}
	}

	ag := agentcore.NewAgent(
		agentcore.WithModel(chatModel),
		agentcore.WithSystemPrompt(systemPrompt),
		agentcore.WithTools(builtTools...),
		agentcore.WithMaxTurns(settings.MaxTurns),
		agentcore.WithMaxToolConcurrency(4),
		agentcore.WithContextPipeline(
			memory.NewCompaction(memory.CompactionConfig{
				Model:         chatModel,
				ContextWindow: settings.ContextWindow,
			}),
			memory.CompactionConvertToLLM,
		),
		agentcore.WithContextWindow(settings.ContextWindow),
		agentcore.WithContextEstimate(memory.ContextEstimateAdapter),
		agentcore.WithPermission(pol.Permission),
	)

	// Bind background task notifier so completed tasks are injected as follow-up messages.
	subagentTool.SetNotifyFn(ag.FollowUp)

	if len(snapshot.Messages) > 0 {
		if err := ag.SetMessages(snapshot.Messages); err != nil {
			return nil, fmt.Errorf("restore agent messages: %w", err)
		}
	}
	if snapshot.Thinking != "" {
		ag.SetThinkingLevel(agentcore.ThinkingLevel(snapshot.Thinking))
		settings.ThinkingLevel = snapshot.Thinking
	} else if settings.ThinkingLevel != "" {
		ag.SetThinkingLevel(agentcore.ThinkingLevel(settings.ThinkingLevel))
	}

	settings.Provider = activeProvider
	settings.Model = activeModel

	sess := agent.NewSession(agent.SessionConfig{
		Agent:        ag,
		Store:        store,
		Manager:      manager,
		Registry:     registry,
		Settings:     settings,
		Cwd:          cwd,
		CreateModel:  createModel,
		ChatModel:    chatModel,
		Tools:        builtTools,
		ContextFiles: ctxFiles,
		Skills:       skills,
	})
	closeStoreOnError = false

	// Before each agent turn, refresh MCP tools if any server signaled a change.
	if mcpManager != nil {
		sess.SetBeforePrompt(func() {
			mcpTools, ok := mcpManager.RefreshIfDirty(context.Background())
			if !ok {
				return
			}
			all := make([]agentcore.Tool, len(baseTools), len(baseTools)+len(mcpTools))
			copy(all, baseTools)
			all = append(all, mcpTools...)
			sess.ReplaceAllTools(all)
		})
	}

	go localtools.CleanOldOutputs()

	return &Runtime{
		Cwd:           cwd,
		GitBranch:     detectGitBranch(cwd),
		PolicyProfile: profile,
		Settings:      settings,
		Session:       sess,
		MCPManager:    mcpManager,
	}, nil
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

func resolveSession(mgr *storage.Manager, cwd string, cont, resume bool, path string, nonTTY bool) (*storage.Store, error) {
	switch {
	case path != "":
		return mgr.OpenPath(path)
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

func parseProfile(s string) (policy.Profile, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return policy.ProfileBalanced, nil
	case string(policy.ProfileOff):
		return policy.ProfileOff, nil
	case string(policy.ProfileStrict):
		return policy.ProfileStrict, nil
	case string(policy.ProfileBalanced):
		return policy.ProfileBalanced, nil
	default:
		return "", fmt.Errorf("invalid policy profile %q (allowed: off, balanced, strict)", s)
	}
}

func fileAuditor(path string) func(policy.AuditEntry) {
	var mu sync.Mutex
	return func(e policy.AuditEntry) {
		entry := map[string]any{
			"time":    e.Time.Format(time.RFC3339Nano),
			"profile": string(e.Profile),
			"tool":    e.Tool,
			"args":    e.Args,
			"allow":   e.Allow,
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
