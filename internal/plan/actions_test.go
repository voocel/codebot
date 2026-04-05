package plan

import "testing"

func TestParseAllowedCommandsNormalizesAndDeduplicates(t *testing.T) {
	t.Parallel()

	commands := ParseAllowedCommands([]RawAllowedCommand{
		{CommandPrefix: "  go   test\t./...  ", Description: "运行测试"},
		{CommandPrefix: "go test ./...", Description: "重复"},
		{CommandPrefix: "go mod tidy"},
	})

	if len(commands) != 2 {
		t.Fatalf("len(commands) = %d, want 2", len(commands))
	}
	if commands[0].CommandPrefix != "go test ./..." {
		t.Fatalf("first prefix = %q, want %q", commands[0].CommandPrefix, "go test ./...")
	}
	if commands[1].Description != "go mod tidy" {
		t.Fatalf("second description = %q, want fallback to prefix", commands[1].Description)
	}
}

func TestParseAllowedCommandsRejectsUnsafeOrTooBroadPrefixes(t *testing.T) {
	t.Parallel()

	commands := ParseAllowedCommands([]RawAllowedCommand{
		{CommandPrefix: "go"},
		{CommandPrefix: "bash -c go test ./..."},
		{CommandPrefix: "go test ./... && go build ./..."},
		{CommandPrefix: "go test ./..."},
	})

	if len(commands) != 1 {
		t.Fatalf("len(commands) = %d, want 1", len(commands))
	}
	if commands[0].CommandPrefix != "go test ./..." {
		t.Fatalf("prefix = %q, want %q", commands[0].CommandPrefix, "go test ./...")
	}
}

func TestParseAllowedCommandsCapsAtFive(t *testing.T) {
	t.Parallel()

	commands := ParseAllowedCommands([]RawAllowedCommand{
		{CommandPrefix: "go test ./..."},
		{CommandPrefix: "go mod tidy"},
		{CommandPrefix: "go build ./..."},
		{CommandPrefix: "pnpm test"},
		{CommandPrefix: "pnpm build"},
		{CommandPrefix: "cargo test"},
	})

	if len(commands) != 5 {
		t.Fatalf("len(commands) = %d, want 5", len(commands))
	}
	if commands[4].CommandPrefix != "pnpm build" {
		t.Fatalf("last prefix = %q, want %q", commands[4].CommandPrefix, "pnpm build")
	}
}
