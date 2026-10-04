package main

import (
	"flag"
	"fmt"
	"os"
	"runtime/debug"

	"github.com/voocel/codebot/internal/acp"
	"github.com/voocel/codebot/internal/app"
	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/interact"
	"github.com/voocel/codebot/internal/ui/commands"
	"github.com/voocel/codebot/internal/ui/print"
	"github.com/voocel/codebot/internal/ui/tui"
)

// Set via ldflags by GoReleaser. Defaults are fallbacks for `go build` /
// `go install`, where fillBuildInfo fills these in from build info.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// fillBuildInfo backfills version, commit, and date from the build's embedded
// info when ldflags did not inject them (`go build` / `go install`). The
// ldflags-injected values from GoReleaser always take precedence.
func fillBuildInfo() {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	if version == "dev" {
		if v := info.Main.Version; v != "" && v != "(devel)" {
			version = v
		}
	}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			if commit == "none" && s.Value != "" {
				commit = s.Value
			}
		case "vcs.time":
			if date == "unknown" && s.Value != "" {
				date = s.Value
			}
		}
	}
}

func main() {
	versionFlag := flag.Bool("version", false, "Print version and exit")
	printFlag := flag.Bool("p", false, "Print mode (non-interactive, pipe-friendly)")
	jsonFlag := flag.Bool("json", false, "JSON output mode (implies -p)")
	continueFlag := flag.Bool("c", false, "Continue most recent session")
	resumeFlag := flag.Bool("r", false, "Select a session to resume")
	modeFlag := flag.String("mode", "balanced", "Permission mode: strict, balanced, accept-edits, trust")
	acpFlag := flag.Bool("acp", false, "Run as an ACP (Agent Client Protocol) agent over stdio")
	setupFlag := flag.Bool("setup", false, "Run the setup wizard (provider + model + API key)")
	flag.Parse()

	fillBuildInfo()

	if *versionFlag {
		fmt.Printf("codebot %s (%s %s)\n", version, commit[:min(7, len(commit))], date)
		return
	}

	printMode := *printFlag || *jsonFlag
	interactive := !printMode && !*acpFlag
	cwd, err := os.Getwd()
	if err != nil {
		fail(err, "error")
	}

	// First-run onboarding happens before Boot: the wizard writes
	// ~/.codebot/settings.json, then the normal boot path picks it up.
	if interactive {
		if *setupFlag || config.NeedsSetup(cwd) {
			result, err := tui.RunOnboarding()
			if err != nil {
				fail(err, "error")
			}
			if !result.Saved {
				fmt.Println("Setup cancelled — run codebot again anytime.")
				return
			}
		}
	} else if *setupFlag {
		fmt.Fprintln(os.Stderr, "-setup requires an interactive terminal")
		os.Exit(1)
	}

	mode, err := interact.ParseMode(*modeFlag)
	if err != nil {
		fail(err, "error")
	}
	resume, err := chooseSession(cwd, *continueFlag, *resumeFlag, interactive)
	if err != nil {
		fail(err, "error")
	}
	opts := app.Options{Cwd: cwd, Mode: mode, Resume: resume}
	if err := run(opts, printMode, *acpFlag, *jsonFlag); err != nil {
		fail(err, "error")
	}
}

// run boots the App for the chosen frontend and runs it.
func run(opts app.Options, printMode, acpMode, jsonMode bool) error {
	switch {
	case acpMode:
		srv := acp.NewServer(version)
		opts.UI, opts.FS, opts.CacheTTL = srv, srv.FS(), "1h"
		a := boot(opts)
		defer a.Close()
		return srv.Serve(a)
	case printMode:
		opts.UI = print.UI{}
		a := boot(opts)
		defer a.Close()
		return print.Run(a, flag.Args(), jsonMode)
	default:
		screen := &tui.UI{}
		opts.UI, opts.Interactive, opts.CacheTTL = screen, true, "1h"
		a := boot(opts)
		defer a.Close()
		return tui.Run(a, screen, commands.New(a, version), version)
	}
}

func boot(opts app.Options) *app.App {
	a, err := app.Boot(opts)
	if err != nil {
		fail(err, "boot error")
	}
	return a
}

func fail(err error, prefix string) {
	fmt.Fprintln(os.Stderr, formatError(err, prefix))
	os.Exit(1)
}
