package main

import (
	"flag"
	"fmt"
	"github.com/voocel/codebot/internal/mode/printmode"
	"github.com/voocel/codebot/internal/mode/tuimode"
	"github.com/voocel/codebot/internal/runtime"
	"os"
)

func main() {
	providerFlag := flag.String("provider", "", "LLM provider: openai, anthropic, gemini")
	modelFlag := flag.String("model", "", "Model name (default: auto per provider)")
	apiKeyFlag := flag.String("api-key", "", "API key (default: from env)")
	baseURLFlag := flag.String("base-url", "", "API base URL (default: from env)")
	printFlag := flag.Bool("p", false, "Print mode (non-interactive, pipe-friendly)")
	jsonFlag := flag.Bool("json", false, "JSON output mode (implies -p)")
	continueFlag := flag.Bool("c", false, "Continue most recent session")
	resumeFlag := flag.Bool("r", false, "Select a session to resume")
	sessionFlag := flag.String("session", "", "Session file path to resume")
	policyProfileFlag := flag.String("policy-profile", "balanced", "Policy profile: strict, balanced, off")
	flag.Parse()

	printMode := *printFlag || *jsonFlag

	rt, err := runtime.Boot(runtime.Options{
		Provider:      *providerFlag,
		Model:         *modelFlag,
		APIKey:        *apiKeyFlag,
		BaseURL:       *baseURLFlag,
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
	if err := tuimode.Run(rt.Session, rt.Agent, rt.Cwd, rt.GitBranch, modelName, rt.PolicyProfile); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
