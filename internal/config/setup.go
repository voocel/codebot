package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ModelOption represents a selectable model for the setup wizard.
type ModelOption struct {
	ID   string
	Name string
}

// ModelLister returns available models for a given provider.
type ModelLister func(provider string) []ModelOption

var providerList = []struct {
	key  string
	name string
}{
	{"openai", "OpenAI"},
	{"anthropic", "Anthropic"},
	{"gemini", "Google Gemini"},
}

// RunSetup runs an interactive first-time configuration wizard.
func RunSetup(settings Resolved, listModels ModelLister) error {
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

	var provName, provDisplayName, provType string
	if providerIdx <= len(providerList) {
		p := providerList[providerIdx-1]
		provName = p.key
		provDisplayName = p.name
		provType = "" // auto-inferred from name
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
	}

	fmt.Printf("\nEnter %s API key: ", provDisplayName)
	apiKey := readLine(reader)
	if apiKey == "" {
		return fmt.Errorf("API key is required")
	}

	fmt.Print("\nEnter base URL (optional, press Enter to skip): ")
	baseURL := readLine(reader)

	// Model selection.
	model := ""
	var models []string
	if listModels != nil {
		modelOpts := listModels(provName)
		if len(modelOpts) > 0 {
			fmt.Println("\nSelect default model:")
			for i, m := range modelOpts {
				fmt.Printf("  %d. %s (%s)\n", i+1, m.ID, m.Name)
			}
			fmt.Print("\n> ")
			modelIdx := readChoice(reader, len(modelOpts), 1)
			model = modelOpts[modelIdx-1].ID
		}
	}
	if model == "" {
		fmt.Print("\nEnter default model name: ")
		model = readLine(reader)
	}
	if model == "" {
		resolvedType := provType
		if resolvedType == "" {
			resolvedType = ProviderConfig{}.ProviderType(provName)
		}
		model = DefaultModelName(resolvedType)
	}

	// Models list.
	fmt.Print("\nEnter available models (comma-separated, or Enter to skip): ")
	modelsInput := readLine(reader)
	if modelsInput != "" {
		for _, m := range strings.Split(modelsInput, ",") {
			m = strings.TrimSpace(m)
			if m != "" {
				models = append(models, m)
			}
		}
	}
	// Ensure default model is in the list.
	if len(models) > 0 && !containsModel(models, model) {
		models = append([]string{model}, models...)
	}

	// Small model.
	fmt.Print("\nEnter small model for sub-agents (optional, press Enter to skip): ")
	smallModel := readLine(reader)

	// Build config.
	pc := &ProviderConfig{APIKey: apiKey, Models: models, SmallModel: smallModel}
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

	fmt.Printf("\nSettings saved to %s\n\n", filepath.Join(UserConfigDir(), "settings.json"))
	return nil
}

func containsModel(models []string, target string) bool {
	for _, m := range models {
		if strings.EqualFold(m, target) {
			return true
		}
	}
	return false
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
