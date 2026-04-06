package plugin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/voocel/codebot/internal/config"
	"github.com/voocel/codebot/internal/skill"
)

const (
	ScopeProject = "project"
	ScopeUser    = "user"
)

var pluginIDNormalizer = regexp.MustCompile(`[^a-z0-9]+`)

type ScaffoldInput struct {
	Cwd   string
	ID    string
	Scope string
}

type ScaffoldResult struct {
	ID           string
	Scope        string
	RootDir      string
	ManifestPath string
}

func NormalizeID(raw string) string {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	normalized = pluginIDNormalizer.ReplaceAllString(normalized, "-")
	return strings.Trim(normalized, "-")
}

func ValidateID(id string) error {
	if !skill.ValidName(id) {
		return fmt.Errorf("plugin id %q is invalid; use lowercase letters, digits, '-' or '_'", id)
	}
	return nil
}

func Scaffold(input ScaffoldInput) (*ScaffoldResult, error) {
	id := NormalizeID(input.ID)
	if id == "" {
		return nil, fmt.Errorf("plugin id is required")
	}
	if err := ValidateID(id); err != nil {
		return nil, err
	}

	scope := strings.ToLower(strings.TrimSpace(input.Scope))
	if scope == "" {
		scope = ScopeProject
	}

	rootDir, err := scaffoldRootDir(input.Cwd, scope, id)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(rootDir); err == nil {
		return nil, fmt.Errorf("plugin %q already exists at %s", id, rootDir)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("stat plugin dir %s: %w", rootDir, err)
	}

	if err := os.MkdirAll(filepath.Join(rootDir, "skills"), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir skills dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(rootDir, "commands"), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir commands dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(rootDir, "skills", ".keep"), nil, 0o644); err != nil {
		return nil, fmt.Errorf("write skills keep file: %w", err)
	}
	if err := os.WriteFile(filepath.Join(rootDir, "commands", ".keep"), nil, 0o644); err != nil {
		return nil, fmt.Errorf("write commands keep file: %w", err)
	}

	manifest := Manifest{
		ID:          id,
		Name:        displayNameFromID(id),
		Version:     "0.1.0",
		Description: "TODO: 描述这个 plugin 为 codebot 增加了什么能力",
		SkillsDir:   "./skills",
		CommandsDir: "./commands",
	}
	manifestPath := filepath.Join(rootDir, "plugin.json")
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal plugin manifest: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(manifestPath, data, 0o644); err != nil {
		return nil, fmt.Errorf("write plugin manifest: %w", err)
	}

	readme := buildScaffoldREADME(id, scope)
	if err := os.WriteFile(filepath.Join(rootDir, "README.md"), []byte(readme), 0o644); err != nil {
		return nil, fmt.Errorf("write plugin readme: %w", err)
	}

	return &ScaffoldResult{
		ID:           id,
		Scope:        scope,
		RootDir:      rootDir,
		ManifestPath: manifestPath,
	}, nil
}

func scaffoldRootDir(cwd, scope, id string) (string, error) {
	switch scope {
	case ScopeProject:
		return filepath.Join(cwd, config.ConfigDir, "plugins", id), nil
	case ScopeUser:
		userDir := config.UserConfigDir()
		if userDir == "" {
			return "", fmt.Errorf("cannot resolve user config dir")
		}
		return filepath.Join(userDir, "plugins", id), nil
	default:
		return "", fmt.Errorf("unknown plugin scope %q", scope)
	}
}

func displayNameFromID(id string) string {
	parts := strings.FieldsFunc(id, func(r rune) bool {
		return r == '-' || r == '_'
	})
	for i, part := range parts {
		if part == "" {
			continue
		}
		parts[i] = strings.ToUpper(part[:1]) + part[1:]
	}
	return strings.Join(parts, " ")
}

func buildScaffoldREADME(id, scope string) string {
	return fmt.Sprintf(
		"# %s\n\n"+
			"这是一个 codebot plugin 脚手架。\n\n"+
			"## 目录\n\n"+
			"- `plugin.json`：plugin manifest\n"+
			"- `skills/`：放技能文件，文件名会成为技能名\n"+
			"- `commands/`：放 slash command 的 Markdown 文件\n\n"+
			"## 当前作用域\n\n"+
			"- %s\n\n"+
			"## 下一步\n\n"+
			"1. 编辑 `plugin.json`，补全描述与版本信息。\n"+
			"2. 在 `skills/` 中新增一个技能文件，例如 `review.md`。\n"+
			"3. 在 `commands/` 中新增一个命令文件，例如 `triage.md`。\n"+
			"4. 回到项目后执行 `/reload` 或 `/plugins list` 检查装载结果。\n\n"+
			"## 最小技能示例\n\n"+
			"```md\n"+
			"---\n"+
			"description: 代码审查助手\n"+
			"when_to_use: 当用户要求你 review diff、找回归或指出风险时使用\n"+
			"---\n\n"+
			"先阅读变更，再给出按严重级别排序的问题清单。\n"+
			"```\n\n"+
			"## 最小命令示例\n\n"+
			"```md\n"+
			"---\n"+
			"description: 进入发布检查流程\n"+
			"usage: /release-check [version]\n"+
			"---\n\n"+
			"检查当前工作区的变更、测试状态和待发布风险。\n"+
			"```\n",
		id,
		scope,
	)
}
