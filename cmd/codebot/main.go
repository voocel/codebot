package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/voocel/codebot/internal/bootstrap"
	"github.com/voocel/codebot/internal/ui"
)

// Set via ldflags by GoReleaser.
var (
	version = "v0.1.2"
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
