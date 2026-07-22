package bootstrap

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
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
	telemetryShutdown func(context.Context) error
	telemetryTracer   *telemetry.Tracer
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
	var telemetryTracer *telemetry.Tracer
	if modelFactory == nil {
		hook, tracer, shutdown, err := telemetry.Setup(context.Background(), settings.Telemetry)
		if err != nil {
			return nil, err
		}
		telemetryShutdown = shutdown
		telemetryTracer = tracer
		if hook != nil {
			modelFactory = provider.NewModelFactory(litellm.WithHook(hook))
		} else {
			modelFactory = provider.CreateModel
		}
	}

	if err := ensureProviderSetup(cwd, settings); err != nil {
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
		telemetryShutdown: telemetryShutdown,
		telemetryTracer:   telemetryTracer,
	}, nil
}

// ensureProviderSetup validates that settings.json fully configures the
// active provider. Credentials come exclusively from the config file; the
// interactive first-run wizard runs in main() before Boot (tui.RunOnboarding),
// so by the time we get here anything missing is an error, not a prompt.
func ensureProviderSetup(cwd string, settings config.Resolved) error {
	if !config.GlobalConfigExists() && !config.ProjectConfigExists(cwd) {
		return fmt.Errorf("no configuration found; run codebot -setup to create one: %w", diag.ErrConfig)
	}
	if !hasConfiguredProviderCredentials(settings, settings.Provider) {
		return fmt.Errorf("configuration error: settings.provider=%q is missing or not configured in settings.json: %w", settings.Provider, diag.ErrConfig)
	}
	if settings.Model == "" {
		return fmt.Errorf("configuration error: model is not set in settings.json: %w", diag.ErrConfig)
	}
	return nil
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
