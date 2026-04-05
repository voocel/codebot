package plan

import (
	"strings"

	"github.com/voocel/codebot/internal/storage"
)

const maxAllowedCommands = 5

type RawAllowedCommand struct {
	CommandPrefix string
	Description   string
}

func ParseAllowedCommands(raw []RawAllowedCommand) []AllowedCommand {
	if len(raw) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(raw))
	var out []AllowedCommand
	for _, item := range raw {
		prefix := normalizeCommandPrefix(item.CommandPrefix)
		if prefix == "" || hasDisallowedShellOperator(prefix) || isShellWrapper(prefix) {
			continue
		}
		if _, ok := seen[prefix]; ok {
			continue
		}
		seen[prefix] = struct{}{}
		desc := strings.TrimSpace(item.Description)
		if desc == "" {
			desc = prefix
		}
		out = append(out, AllowedCommand{
			CommandPrefix: prefix,
			Description:   desc,
		})
		if len(out) >= maxAllowedCommands {
			break
		}
	}
	return out
}

func ParseAllowedCommandsFromEntries(entries []storage.AllowedCommandEntry) []AllowedCommand {
	if len(entries) == 0 {
		return nil
	}
	raw := make([]RawAllowedCommand, 0, len(entries))
	for _, entry := range entries {
		raw = append(raw, RawAllowedCommand{
			CommandPrefix: entry.CommandPrefix,
			Description:   entry.Description,
		})
	}
	return ParseAllowedCommands(raw)
}

func AllowedCommandPrefixes(commands []AllowedCommand) []string {
	if len(commands) == 0 {
		return nil
	}
	out := make([]string, 0, len(commands))
	for _, command := range commands {
		out = append(out, command.CommandPrefix)
	}
	return out
}

func AllowedCommandsToEntries(commands []AllowedCommand) []storage.AllowedCommandEntry {
	if len(commands) == 0 {
		return nil
	}
	out := make([]storage.AllowedCommandEntry, 0, len(commands))
	for _, command := range commands {
		out = append(out, storage.AllowedCommandEntry{
			CommandPrefix: command.CommandPrefix,
			Description:   command.Description,
		})
	}
	return out
}

func DescribeAllowedCommands(commands []AllowedCommand) []string {
	if len(commands) == 0 {
		return nil
	}
	out := make([]string, 0, len(commands))
	for _, command := range commands {
		desc := strings.TrimSpace(command.Description)
		if desc == "" || desc == command.CommandPrefix {
			out = append(out, command.CommandPrefix)
			continue
		}
		out = append(out, command.CommandPrefix+" — "+desc)
	}
	return out
}

func normalizeCommandPrefix(raw string) string {
	fields := strings.Fields(strings.TrimSpace(raw))
	if len(fields) < 2 {
		return ""
	}
	return strings.Join(fields, " ")
}

func isShellWrapper(prefix string) bool {
	fields := strings.Fields(prefix)
	if len(fields) < 2 {
		return false
	}
	switch fields[0] {
	case "bash", "sh", "zsh", "fish":
		return fields[1] == "-c"
	case "env":
		return fields[1] == "-c" || (len(fields) >= 3 && fields[2] == "-c")
	default:
		return false
	}
}

func hasDisallowedShellOperator(cmd string) bool {
	return len(splitCommandSegments(cmd)) > 1
}

func splitCommandSegments(cmd string) []string {
	var (
		parts    []string
		buf      strings.Builder
		inSingle bool
		inDouble bool
		escaped  bool
	)

	flush := func() {
		part := strings.TrimSpace(buf.String())
		if part != "" {
			parts = append(parts, part)
		}
		buf.Reset()
	}

	for i := 0; i < len(cmd); i++ {
		ch := cmd[i]
		if escaped {
			buf.WriteByte(ch)
			escaped = false
			continue
		}
		if ch == '\\' && !inSingle {
			escaped = true
			buf.WriteByte(ch)
			continue
		}
		if ch == '\'' && !inDouble {
			inSingle = !inSingle
			buf.WriteByte(ch)
			continue
		}
		if ch == '"' && !inSingle {
			inDouble = !inDouble
			buf.WriteByte(ch)
			continue
		}
		if !inSingle && !inDouble {
			if ch == ';' || ch == '|' {
				flush()
				if ch == '|' && i+1 < len(cmd) && cmd[i+1] == '|' {
					i++
				}
				continue
			}
			if ch == '&' && i+1 < len(cmd) && cmd[i+1] == '&' {
				flush()
				i++
				continue
			}
		}
		buf.WriteByte(ch)
	}

	flush()
	return parts
}
