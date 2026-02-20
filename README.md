# Codebot

Terminal-native AI coding agent. Built on [agentcore](https://github.com/voocel/agentcore), a minimal agent execution kernel.

```
╭──────────────────────────────────────────────────────╮
│ ◆ Codebot                                            │
│ anthropic/claude-sonnet-4.6 · ~/project (main)       │
│                                                      │
│ Enter send · Ctrl+J newline · Esc abort · /help      │
╰──────────────────────────────────────────────────────╯
```

## Why

Most AI coding tools are either bloated frameworks or thin API wrappers. Codebot sits in between: a **complete agent** with session management, security policies, and a polished TUI — in under 3000 lines of application code.

The trick: **agentcore handles execution, codebot handles coordination.**

Each layer has one job. No layer knows about the layers above it.

## Features

**Agent**
- Streaming responses with extended thinking (off → xhigh)
- Tool execution: read, write, edit, bash, grep, find, ls
- Automatic context compaction when window fills up
- Multi-provider: Anthropic, OpenAI, Gemini

**Sessions**
- Append-only JSONL persistence — crash-safe, human-readable
- Resume (`-c` last, `-r` pick), fork at any point, replay
- Model and thinking level restored per session

**Security**
- Three profiles: `strict` / `balanced` / `off`
- Dangerous command blocking (rm -rf, sudo, dd, ...)
- Workspace-scoped file access
- JSON audit log for every tool decision

**Interface**
- Interactive TUI with real-time streaming and markdown rendering
- Non-interactive print mode for pipes and scripts (`-p`)
- Slash commands: `/model`, `/thinking`, `/compact`, `/fork`, `/resume`, ...

## Quick Start

```bash
# Install globally
go install github.com/voocel/codebot/cmd/codebot@latest

# Or build from source
git clone https://github.com/voocel/codebot.git
cd codebot && go build -o codebot ./cmd/codebot
```

## Usage

```bash
# Interactive TUI
codebot

# Pipe mode
echo "explain main.go" | codebot -p

# Continue last session
codebot -c

# Strict security
codebot -policy-profile strict
```

## Design Principles

1. **Reuse before reinvent** — agentcore does the agent loop, codebot doesn't redo it
2. **No premature abstraction** — every interface has at least two real callers
3. **Convention over configuration** — sensible defaults, explicit overrides
4. **Secure by default** — balanced policy, audit trail, workspace boundaries

## Requirements

- Go 1.25+
- API key for at least one provider (`ANTHROPIC_API_KEY` / `OPENAI_API_KEY` / `GEMINI_API_KEY`)

## License

MIT
