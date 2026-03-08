package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/voocel/codebot/internal/policy"
)

func TestLoadFileCommands_ProjectOverridesUser(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	t.Setenv("HOME", home)

	userDir := filepath.Join(home, ConfigDir, "commands")
	projectDir := CommandsDir(cwd)
	if err := os.MkdirAll(userDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(userDir, "deploy.md"), []byte(`---
description: 用户命令
aliases: [ship]
risk: high
---
用户版本
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "deploy.md"), []byte(`---
description: 项目命令
aliases: [release]
needs-idle: true
---
项目版本
`), 0o644); err != nil {
		t.Fatal(err)
	}

	commands := LoadFileCommands(cwd)
	if len(commands) != 1 {
		t.Fatalf("expected 1 command, got %d", len(commands))
	}

	cmd := commands[0]
	if cmd.Source != "project" {
		t.Fatalf("expected project source, got %q", cmd.Source)
	}
	if cmd.Description != "项目命令" {
		t.Fatalf("expected overridden description, got %q", cmd.Description)
	}
	if cmd.Risk != policy.RiskLow {
		t.Fatalf("expected default low risk, got %q", cmd.Risk)
	}
	if !cmd.NeedsIdle {
		t.Fatal("expected project override to set needs idle")
	}
	if len(cmd.Aliases) != 1 || cmd.Aliases[0] != "release" {
		t.Fatalf("expected project aliases to override user aliases, got %v", cmd.Aliases)
	}
}

func TestLoadFileCommandParsesMetadata(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "commit.md")
	if err := os.WriteFile(path, []byte(`---
name: commit-msg
description: 生成提交信息
usage: /commit-msg <scope>
aliases: [cm, cmsg]
risk: medium
needs_idle: true
hidden: true
---
请根据变更生成提交信息：$@
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd, err := loadFileCommand(path, "project")
	if err != nil {
		t.Fatal(err)
	}

	if cmd.Name != "commit-msg" {
		t.Fatalf("unexpected name: %q", cmd.Name)
	}
	if cmd.Usage != "/commit-msg <scope>" {
		t.Fatalf("unexpected usage: %q", cmd.Usage)
	}
	if cmd.Risk != policy.RiskMedium {
		t.Fatalf("unexpected risk: %q", cmd.Risk)
	}
	if !cmd.NeedsIdle || !cmd.Hidden {
		t.Fatalf("expected needs idle and hidden to be true: %+v", cmd)
	}
	if len(cmd.Aliases) != 2 || cmd.Aliases[0] != "cm" || cmd.Aliases[1] != "cmsg" {
		t.Fatalf("unexpected aliases: %v", cmd.Aliases)
	}
}
