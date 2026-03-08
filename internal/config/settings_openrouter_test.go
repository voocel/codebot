package config

import "testing"

func TestProviderEnvKeyOpenRouter(t *testing.T) {
	t.Parallel()

	if got := ProviderEnvKey("openrouter"); got != "OPENROUTER_API_KEY" {
		t.Fatalf("ProviderEnvKey(openrouter) = %q, want %q", got, "OPENROUTER_API_KEY")
	}
}
