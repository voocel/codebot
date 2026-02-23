package ui

import (
	"fmt"
	"strings"

	"github.com/voocel/codebot/internal/agent"
)

// RunPrint executes non-interactive print/json mode.
func RunPrint(sess *agent.Session, args []string, jsonMode bool) error {
	prompt := strings.Join(args, " ")
	if prompt == "" {
		stdinPrompt, err := ReadStdinPrompt()
		if err != nil {
			return fmt.Errorf("stdin error: %w", err)
		}
		prompt = strings.TrimSpace(stdinPrompt)
	}
	if prompt == "" {
		return fmt.Errorf("print mode requires a prompt (argument or stdin pipe)")
	}
	if err := RunPrintMode(sess, prompt, jsonMode); err != nil {
		return fmt.Errorf("print mode: %w", err)
	}
	return nil
}
