package plugin

import (
	"fmt"
	"strings"

	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/skill"
)

type ValidationReport struct {
	RootDir      string
	Scope        string
	Manifest     Manifest
	State        *State
	SkillsDir    string
	CommandsDir  string
	SkillCount   int
	CommandCount int
	MCPCount     int
	Errors       []string
	Warnings     []string
}

func ValidatePath(path, scope string) (*ValidationReport, error) {
	root, manifest, err := loadInstallSource(path)
	if err != nil {
		return nil, err
	}
	report := &ValidationReport{
		RootDir:  root,
		Scope:    strings.TrimSpace(scope),
		Manifest: manifest,
		MCPCount: len(manifest.MCPServers),
	}

	loaded := Loaded{Manifest: manifest, RootDir: root, Scope: report.Scope}
	if dir := loaded.skillDir(); dir != "" {
		report.SkillsDir = dir
		specs, errs := skill.ValidateDir(dir, "plugin")
		report.SkillCount = len(specs)
		for _, err := range errs {
			report.Errors = append(report.Errors, err.Error())
		}
		if len(specs) == 0 {
			report.Warnings = append(report.Warnings, "skillsDir exists but contains no loadable skill")
		}
	}
	if dir := loaded.commandsDir(); dir != "" {
		report.CommandsDir = dir
		cmds, errs := config.ValidateCommandsDir(dir, "plugin")
		report.CommandCount = len(cmds)
		for _, err := range errs {
			report.Errors = append(report.Errors, err.Error())
		}
		if len(cmds) == 0 {
			report.Warnings = append(report.Warnings, "commandsDir exists but contains no loadable command")
		}
	}
	if report.SkillCount == 0 && report.CommandCount == 0 && report.MCPCount == 0 {
		report.Warnings = append(report.Warnings, "plugin has no contributions")
	}
	return report, nil
}

func ValidateLoaded(loaded Loaded) (*ValidationReport, error) {
	report, err := ValidatePath(loaded.RootDir, loaded.Scope)
	if err != nil {
		return nil, err
	}
	state := loaded.State
	report.State = &state
	if !loaded.IsTrusted() && report.MCPCount > 0 {
		report.Warnings = append(report.Warnings, "trust=untrusted; MCP contributions are filtered out")
	}
	return report, nil
}

func (r *ValidationReport) Summary() string {
	if r == nil {
		return ""
	}
	status := "valid"
	if len(r.Errors) > 0 {
		status = "invalid"
	}
	return fmt.Sprintf("%s: %d skills, %d commands, %d mcp", status, r.SkillCount, r.CommandCount, r.MCPCount)
}
