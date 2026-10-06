package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strconv"
	"strings"

	"github.com/voocel/codebot/internal/app"
	"github.com/voocel/codebot/internal/infra/config"
	"github.com/voocel/codebot/internal/interact"
	"github.com/voocel/codebot/internal/session/storage"
	"github.com/voocel/codebot/internal/ui/acp"
	"github.com/voocel/codebot/internal/ui/print"
	"github.com/voocel/codebot/internal/ui/tui"
	"github.com/voocel/codebot/internal/ui/tui/onboarding"
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
	trustFlag := flag.Bool("trust", false, "Trust this folder for this run: its hooks, MCP servers and allow rules take effect")
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
		if *setupFlag || config.NeedsSetup() {
			result, err := onboarding.Run()
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
	opts := app.Options{Cwd: cwd, Mode: mode, Resume: resume, Trust: *trustFlag}
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
		warn(a)
		return srv.Serve(a)
	case printMode:
		opts.UI = print.UI{}
		a := boot(opts)
		defer a.Close()
		warn(a)
		return print.Run(a, flag.Args(), jsonMode)
	default:
		screen := &tui.UI{}
		opts.UI, opts.Interactive, opts.CacheTTL = screen, true, "1h"
		a := boot(opts)
		defer a.Close()
		return tui.Run(a, screen, version)
	}
}

func boot(opts app.Options) *app.App {
	a, err := app.Boot(opts)
	if err != nil {
		fail(err, "boot error")
	}
	return a
}

// warn tells the user, where nobody can be asked, what of the extensions
// was left out, and what of the folder is off until they trust it.
func warn(a *app.App) {
	for _, err := range a.Extensions().Problems {
		fmt.Fprintln(os.Stderr, "codebot: "+err.Error())
	}
	for _, pl := range a.Plugins() {
		switch pl.State {
		case app.PluginMissing:
			// The others are fetched as codebot connects.
			if pl.Commit == "" && pl.Scope == "user" {
				fmt.Fprintf(os.Stderr, "codebot: plugin %s is not installed: add it with /plugins add in codebot\n", pl.Source)
			}
		case app.PluginBroken:
			fmt.Fprintf(os.Stderr, "codebot: plugin %s is broken: %v\n", pl.Source, pl.Err)
		}
	}
	t := a.Trust()
	if !t.Held() {
		return
	}
	fmt.Fprintf(os.Stderr, "codebot: %s is not trusted, so these are off:\n", t.Root)
	for _, it := range t.Surface {
		fmt.Fprintf(os.Stderr, "  %s  %s\n", it.Kind, it.Detail)
	}
	fmt.Fprintln(os.Stderr, "Trust it with /trust in codebot, or pass --trust for this run.")
}

func fail(err error, prefix string) {
	fmt.Fprintln(os.Stderr, prefix+": "+app.ErrorText(err))
	os.Exit(1)
}

// chooseSession resolves -c and -r to the session to open: "" starts a new
// one. -r asks on the terminal, so it needs one.
func chooseSession(cwd string, latest, pick, interactive bool) (string, error) {
	if !latest && !pick {
		return "", nil
	}
	sessions, err := storage.NewManager(config.SessionsDir(cwd)).List()
	if err != nil {
		return "", err
	}
	if len(sessions) == 0 {
		return "", nil
	}
	if latest {
		return sessions[0].ID, nil
	}
	if !interactive {
		return "", fmt.Errorf("-r requires an interactive terminal, use -c in non-interactive mode")
	}

	fmt.Fprintf(os.Stderr, "Available sessions:\n")
	for i, s := range sessions {
		fmt.Fprintf(os.Stderr, "  %d. %s  %s  %s\n", i+1, s.Updated.Format("2006-01-02 15:04"), s.ID, s.FirstMessage)
	}
	fmt.Fprintf(os.Stderr, "Select session number or id: ")
	raw, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read session selection: %w", err)
	}
	choice := strings.TrimSpace(raw)
	for _, s := range sessions {
		if s.ID == choice {
			return s.ID, nil
		}
	}
	if i, err := strconv.Atoi(choice); err == nil && i >= 1 && i <= len(sessions) {
		return sessions[i-1].ID, nil
	}
	return "", fmt.Errorf("invalid session selection %q", choice)
}
