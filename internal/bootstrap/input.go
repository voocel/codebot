package bootstrap

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/voocel/codebot/internal/agent"
	"github.com/voocel/codebot/internal/approval"
	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/diag"
	"github.com/voocel/codebot/internal/provider"
	"github.com/voocel/codebot/internal/storage"
	"github.com/voocel/codebot/internal/telemetry"
	"github.com/voocel/litellm"
)

type resolvedInput struct {
	cwd               string
	settings          config.Resolved
	registry          *provider.ModelRegistry
	approvalMode      approval.Mode
	modelFactory      agent.ModelFactory
	sessionManager    *storage.Manager
	sessionStore      *storage.Store
	sessionSnapshot   storage.ContextSnapshot
	nonTTY            bool
	envHint           string
	telemetryShutdown func(context.Context) error
}

func resolveInput(opts Options) (*resolvedInput, error) {
	cwd := opts.Cwd
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("get cwd: %w", err)
		}
	}

	settings, err := config.ResolveAllStrict(cwd)
	if err != nil {
		return nil, err
	}
	registry := provider.NewModelRegistry()
	provider.StartPricingRefresh(registry, config.UserConfigDir())

	approvalMode, err := approval.ParseMode(opts.ApprovalMode)
	if err != nil {
		return nil, err
	}

	modelFactory := opts.ModelFactory
	var telemetryShutdown func(context.Context) error
	if modelFactory == nil {
		hook, shutdown, err := telemetry.Setup(context.Background(), settings.Telemetry)
		if err != nil {
			return nil, err
		}
		telemetryShutdown = shutdown
		if hook != nil {
			modelFactory = provider.NewModelFactory(litellm.WithHook(hook))
		} else {
			modelFactory = provider.CreateModel
		}
	}

	settings, envHint, err := ensureProviderSetup(cwd, settings, opts.NonTTYMode)
	if err != nil {
		return nil, err
	}

	sessionManager := storage.NewManager(config.SessionsDir(cwd))
	sessionStore, err := resolveSession(sessionManager, cwd, opts.Continue, opts.Resume, opts.NonTTYMode)
	if err != nil {
		return nil, fmt.Errorf("session setup failed: %w: %w", diag.ErrSession, err)
	}

	var sessionSnapshot storage.ContextSnapshot
	if opts.Continue || opts.Resume {
		sessionSnapshot, err = sessionStore.BuildSnapshot()
		if err != nil {
			_ = sessionStore.Close()
			return nil, fmt.Errorf("restore context failed: %w: %w", diag.ErrSession, err)
		}
	}

	return &resolvedInput{
		cwd:               cwd,
		settings:          settings,
		registry:          registry,
		approvalMode:      approvalMode,
		modelFactory:      modelFactory,
		sessionManager:    sessionManager,
		sessionStore:      sessionStore,
		sessionSnapshot:   sessionSnapshot,
		nonTTY:            opts.NonTTYMode,
		envHint:           envHint,
		telemetryShutdown: telemetryShutdown,
	}, nil
}

func ensureProviderSetup(cwd string, settings config.Resolved, nonTTY bool) (config.Resolved, string, error) {
	configExists := config.GlobalConfigExists() || config.ProjectConfigExists(cwd)
	if configExists {
		if hasConfiguredProviderCredentials(settings, settings.Provider) {
			return settings, "", nil
		}
		return settings, "", fmt.Errorf("configuration error: settings.provider=%q is missing or not configured in settings.json: %w", settings.Provider, diag.ErrConfig)
	}

	apiKey, _ := settings.ProviderCredentials(settings.Provider)
	if apiKey != "" {
		return settings, envHintFor(settings), nil
	}

	if prov, envKey := config.DetectEnvProvider(); prov != "" {
		settings.Provider = prov
		settings.Model = config.DefaultModelName(prov)
		settings.SmallModel = settings.Model
		return settings, fmt.Sprintf("Using %s from environment", envKey), nil
	}

	if nonTTY {
		return settings, "", fmt.Errorf("api key not set; set %s or configure providers in %s: %w",
			config.ProviderEnvKey(settings.Provider), filepath.Join(config.UserConfigDir(), "settings.json"), diag.ErrConfig)
	}

	if err := config.RunSetup(settings); err != nil {
		return settings, "", fmt.Errorf("setup failed: %w: %w", diag.ErrConfig, err)
	}
	resolved, err := config.ResolveAllStrict(cwd)
	if err != nil {
		return settings, "", err
	}
	return resolved, "", nil
}

func envHintFor(settings config.Resolved) string {
	if pc, ok := settings.Providers[settings.Provider]; ok && pc.APIKey != "" {
		return ""
	}
	return fmt.Sprintf("Using %s from environment", config.ProviderEnvKey(settings.Provider))
}

func hasConfiguredProviderCredentials(settings config.Resolved, provider string) bool {
	pc, ok := settings.Providers[provider]
	return ok && pc.APIKey != ""
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
			return nil, fmt.Errorf("-r requires interactive terminal, use -c or -session in non-interactive mode: %w", diag.ErrSession)
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
			return nil, fmt.Errorf("read session selection failed: %w: %w", diag.ErrSession, err)
		}
		choice := strings.TrimSpace(raw)
		if choice == "" {
			return nil, fmt.Errorf("no session selected: %w", diag.ErrSession)
		}

		for _, s := range sessions {
			if s.ID == choice {
				return mgr.OpenPath(s.Path)
			}
		}

		if idx, convErr := strconv.Atoi(choice); convErr == nil {
			if idx < 1 || idx > len(sessions) {
				return nil, fmt.Errorf("invalid session index %d (range: 1-%d): %w", idx, len(sessions), diag.ErrSession)
			}
			return mgr.OpenPath(sessions[idx-1].Path)
		}
		return nil, fmt.Errorf("invalid session selection %q: %w", choice, diag.ErrSession)
	default:
		return mgr.Create(cwd)
	}
}
