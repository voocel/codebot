package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/voocel/codebot/internal/apperr"
	"github.com/voocel/codebot/internal/bootstrap"
	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/ui"
)

// Set via ldflags by GoReleaser.
var (
	version = "v0.1.0"
	commit  = "none"
	date    = "unknown"
)

func main() {
	versionFlag := flag.Bool("version", false, "Print version and exit")
	printFlag := flag.Bool("p", false, "Print mode (non-interactive, pipe-friendly)")
	jsonFlag := flag.Bool("json", false, "JSON output mode (implies -p)")
	continueFlag := flag.Bool("c", false, "Continue most recent session")
	resumeFlag := flag.Bool("r", false, "Select a session to resume")
	modeFlag := flag.String("mode", "balanced", "Permission mode: strict, balanced, accept-edits, trust")
	flag.Parse()

	if *versionFlag {
		fmt.Printf("codebot %s (%s %s)\n", version, commit[:min(7, len(commit))], date)
		return
	}

	printMode := *printFlag || *jsonFlag

	rt, err := bootstrap.Boot(bootstrap.Options{
		Continue:     *continueFlag,
		Resume:       *resumeFlag,
		NonTTYMode:   printMode,
		ApprovalMode: *modeFlag,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, formatBootError(err))
		os.Exit(1)
	}
	defer rt.Close()

	if printMode {
		if err := ui.RunPrint(rt.Session, flag.Args(), *jsonFlag); err != nil {
			fmt.Fprintln(os.Stderr, formatCLIError(err))
			os.Exit(1)
		}
		return
	}

	modelName := config.FormatModelID(rt.Settings.Provider, rt.Settings.Model)
	if rt.Session != nil && rt.Session.ModelName() != "" {
		modelName = rt.Session.ModelName()
	}
	if err := ui.RunTUI(rt.Session, rt.Cwd, rt.GitBranch, modelName, version, rt.ApprovalEngine, rt.TaskRuntime, rt.MCPManager, rt.MCPServers, rt.PluginCatalog, rt.SkillCatalog, rt.EnvHint, rt.SessionStore, rt.PlanSlug, rt.PlanTitle, rt.PlanPhase, rt.PlanPreMode, rt.PlanAllowedCommands); err != nil {
		fmt.Fprintln(os.Stderr, formatCLIError(err))
		os.Exit(1)
	}
}

func formatBootError(err error) string {
	return apperr.Format(err, "boot error")
}

func formatCLIError(err error) string {
	return apperr.Format(err, "error")
}
