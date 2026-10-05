package plugin

import (
	"fmt"
	"strings"

	"github.com/voocel/codebot/internal/agent/skill"
)

type ValidationReport struct {
	RootDir    string
	Scope      string
	Manifest   Manifest
	State      *State
	SkillCount int
	MCPCount   int
	Errors     []string
	Warnings   []string
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
		specs, errs := skill.LoadDir(dir)
		report.SkillCount = len(specs)
		for _, err := range errs {
			report.Errors = append(report.Errors, err.Error())
		}
		if len(specs) == 0 {
			report.Warnings = append(report.Warnings, "skillsDir exists but contains no loadable skill")
		}
	}
	if report.SkillCount == 0 && report.MCPCount == 0 {
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
	return fmt.Sprintf("%s: %d skills, %d mcp", status, r.SkillCount, r.MCPCount)
}
