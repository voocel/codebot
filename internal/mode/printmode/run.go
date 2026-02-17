package printmode

import (
	"fmt"
	"strings"

	"github.com/voocel/codebot/internal/agent"
	"github.com/voocel/codebot/internal/app"
)

// Run executes non-interactive print/json mode.
func Run(sess agent.Session, args []string, jsonMode bool) error {
	prompt := strings.Join(args, " ")
	if prompt == "" {
		stdinPrompt, err := app.ReadStdinPrompt()
		if err != nil {
			return fmt.Errorf("stdin error: %w", err)
		}
		prompt = strings.TrimSpace(stdinPrompt)
	}
	if prompt == "" {
		return fmt.Errorf("print mode requires a prompt (argument or stdin pipe)")
	}
	if err := app.RunPrintMode(sess, prompt, jsonMode); err != nil {
		return fmt.Errorf("print mode: %w", err)
	}
	return nil
}
