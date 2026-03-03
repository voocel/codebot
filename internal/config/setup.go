package config

import (
	"bufio"
	"fmt"
	"os"
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
func RunSetup(cwd string, settings Resolved, listModels ModelLister) error {
	reader := bufio.NewReader(os.Stdin)

	fmt.Println("\nWelcome to codebot! Let's configure your settings.")

	fmt.Println("Select provider:")
	for i, p := range providerList {
		marker := ""
		if p.key == settings.Provider {
			marker = " (current)"
		}
		fmt.Printf("  %d. %s%s\n", i+1, p.name, marker)
	}
	fmt.Print("\n> ")
	providerIdx := readChoice(reader, len(providerList), 1)
	prov := providerList[providerIdx-1].key

	fmt.Printf("\nEnter %s API key: ", providerList[providerIdx-1].name)
	apiKey := readLine(reader)
	if apiKey == "" {
		return fmt.Errorf("API key is required")
	}

	fmt.Print("\nEnter base URL (optional, press Enter to skip): ")
	baseURL := readLine(reader)

	model := ""
	if listModels != nil {
		models := listModels(prov)
		if len(models) > 0 {
			fmt.Println("\nSelect default model:")
			for i, m := range models {
				fmt.Printf("  %d. %s (%s)\n", i+1, m.ID, m.Name)
			}
			fmt.Print("\n> ")
			modelIdx := readChoice(reader, len(models), 1)
			model = models[modelIdx-1].ID
		}
	}

	pc := &ProviderConfig{APIKey: apiKey}
	if baseURL != "" {
		pc.BaseURL = baseURL
	}

	s := Settings{
		Provider:  &prov,
		Providers: map[string]*ProviderConfig{prov: pc},
	}
	if model == "" {
		model = DefaultModelName(prov)
	}
	s.Model = &model

	if err := SaveSettings(cwd, s); err != nil {
		return fmt.Errorf("save settings: %w", err)
	}

	fmt.Printf("\nSettings saved to %s\n\n", SettingsPath(cwd))
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
