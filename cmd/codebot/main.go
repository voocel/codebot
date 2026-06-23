package main

import (
	"flag"
	"fmt"
	"os"
	"runtime/debug"

	"github.com/voocel/codebot/internal/acp"
	"github.com/voocel/codebot/internal/bootstrap"
	"github.com/voocel/codebot/internal/ui"
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
	flag.Parse()

	fillBuildInfo()

	if *versionFlag {
		fmt.Printf("codebot %s (%s %s)\n", version, commit[:min(7, len(commit))], date)
		return
	}

	printMode := *printFlag || *jsonFlag
	acpMode := *acpFlag

	opts := bootstrap.Options{
		Continue:     *continueFlag,
		Resume:       *resumeFlag,
		NonTTYMode:   printMode || acpMode,
		ApprovalMode: *modeFlag,
	}
	// In ACP mode, route file read/write through the editor (unsaved buffers)
	// by injecting an editor-backed WorkspaceFS. The connection is bound later
	// in acp.Serve; until then it transparently uses the local filesystem.
	var acpFS *acp.WorkspaceFS
	if acpMode {
		acpFS = acp.NewWorkspaceFS()
		opts.WorkspaceFS = acpFS
	}

	rt, err := bootstrap.Boot(opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, formatBootError(err))
		os.Exit(1)
	}
	defer rt.Close()

	if acpMode {
		if err := acp.Serve(rt, version, acpFS); err != nil {
			fmt.Fprintln(os.Stderr, formatCLIError(err))
			os.Exit(1)
		}
		return
	}

	if printMode {
		if err := ui.RunPrint(rt, flag.Args(), *jsonFlag); err != nil {
			fmt.Fprintln(os.Stderr, formatCLIError(err))
			os.Exit(1)
		}
		return
	}

	if err := ui.RunTUI(rt, version); err != nil {
		fmt.Fprintln(os.Stderr, formatCLIError(err))
		os.Exit(1)
	}
}

func formatBootError(err error) string {
	return ui.FormatError(err, "boot error")
}

func formatCLIError(err error) string {
	return ui.FormatError(err, "error")
}
