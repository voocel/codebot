package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/voocel/codebot/internal/mode/printmode"
	"github.com/voocel/codebot/internal/mode/tuimode"
	"github.com/voocel/codebot/internal/runtime"
)

func main() {
	printFlag := flag.Bool("p", false, "Print mode (non-interactive, pipe-friendly)")
	jsonFlag := flag.Bool("json", false, "JSON output mode (implies -p)")
	continueFlag := flag.Bool("c", false, "Continue most recent session")
	resumeFlag := flag.Bool("r", false, "Select a session to resume")
	sessionFlag := flag.String("session", "", "Session file path to resume")
	policyProfileFlag := flag.String("policy-profile", "balanced", "Policy profile: strict, balanced, off")
	flag.Parse()

	printMode := *printFlag || *jsonFlag

	rt, err := runtime.Boot(runtime.Options{
		Continue:      *continueFlag,
		Resume:        *resumeFlag,
		Session:       *sessionFlag,
		NonTTYMode:    printMode,
		PolicyProfile: *policyProfileFlag,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "boot error: %v\n", err)
		os.Exit(1)
	}
	defer rt.Close()

	if printMode {
		if err := printmode.Run(rt.Session, flag.Args(), *jsonFlag); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	modelName := rt.Settings.DefaultModel
	if rt.Session != nil && rt.Session.ModelName() != "" {
		modelName = rt.Session.ModelName()
	}
	if err := tuimode.Run(rt.Session, rt.Cwd, rt.GitBranch, modelName, rt.PolicyProfile); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
