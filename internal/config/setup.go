package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var providerList = []struct {
	key  string
	name string
}{
	{"openai", "OpenAI"},
	{"anthropic", "Anthropic"},
	{"gemini", "Google Gemini"},
	{"openrouter", "OpenRouter"},
}

// RunSetup runs an interactive first-time configuration wizard.
func RunSetup(settings Resolved) error {
	reader := bufio.NewReader(os.Stdin)

	fmt.Println("\nWelcome to codebot! Let's configure your settings.")

	// Provider selection: predefined + custom option.
	fmt.Println("Select provider:")
	for i, p := range providerList {
		marker := ""
		if p.key == settings.Provider {
			marker = " (current)"
		}
		fmt.Printf("  %d. %s%s\n", i+1, p.name, marker)
	}
	fmt.Printf("  %d. Other (custom)\n", len(providerList)+1)
	fmt.Print("\n> ")
	totalChoices := len(providerList) + 1
	providerIdx := readChoice(reader, totalChoices, 1)

	var provName, provDisplayName, provType, baseURL string
	if providerIdx <= len(providerList) {
		p := providerList[providerIdx-1]
		provName = p.key
		provDisplayName = p.name
	} else {
		// Custom provider.
		fmt.Print("\nProvider name (e.g. my-proxy): ")
		provName = readLine(reader)
		if provName == "" {
			return fmt.Errorf("provider name is required")
		}
		provDisplayName = provName

		fmt.Print("Protocol type (openai/anthropic/gemini) [openai]: ")
		provType = strings.ToLower(readLine(reader))
		switch provType {
		case "", "openai":
			provType = "openai"
		case "anthropic", "gemini":
			// valid
		default:
			return fmt.Errorf("unsupported protocol type %q (allowed: openai, anthropic, gemini)", provType)
		}

		fmt.Print("Base URL: ")
		baseURL = readLine(reader)
	}

	fmt.Printf("\nEnter %s API key: ", provDisplayName)
	apiKey := readLine(reader)
	if apiKey == "" {
		return fmt.Errorf("API key is required")
	}

	// Auto-resolve default model.
	resolvedType := provType
	if resolvedType == "" {
		resolvedType = ProviderConfig{}.ProviderType(provName)
	}
	model := DefaultModelName(resolvedType)

	// Build config.
	pc := &ProviderConfig{APIKey: apiKey, Models: []string{model}}
	if provType != "" {
		pc.Type = provType
	}
	if baseURL != "" {
		pc.BaseURL = baseURL
	}

	s := Settings{
		Provider:  &provName,
		Model:     &model,
		Providers: map[string]*ProviderConfig{provName: pc},
	}

	if err := SaveSettings(s); err != nil {
		return fmt.Errorf("save settings: %w", err)
	}

	fmt.Printf("\nSettings saved to %s\n", filepath.Join(UserConfigDir(), "settings.json"))
	fmt.Printf("Using model: %s (edit settings.json to customize)\n\n", model)
	return nil
}

func readLine(reader *bufio.Reader) string {
	line, _ := reader.ReadString('\n')
	return strings.TrimSpace(line)
}

func readChoice(reader *bufio.Reader, max, defaultVal int) int {
	line := readLine(reader)
	if line == "" {
		return defaultVal
	}
	var n int
	if _, err := fmt.Sscanf(line, "%d", &n); err != nil || n < 1 || n > max {
		return defaultVal
	}
	return n
}
