# Codebot

[English](README.md) | [中文](README_zh.md)

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
- Tool execution: read, write, edit, bash, grep, find, ls, web_search, web_fetch
- Task management: task_create, task_get, task_update, task_list (SubAgent coordination)
- SubAgent delegation with parallel/chain execution
- Automatic context compaction when window fills up
- Multi-provider: Anthropic, OpenAI, Gemini
- MCP (Model Context Protocol) server integration

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
- Plan mode: agent proposes changes, user reviews and approves
- AskUser: structured multi-choice questions from agent to user
- Image paste (Ctrl+V) with selection (↑) and deletion (Delete)
- Task progress display: progress bar + status icons above input
- Non-interactive print mode for pipes and scripts (`-p`)
- Slash commands: `/model`, `/compact`, `/plan`, `/resume`, `/copy`, ...

## Quick Start

```bash
# Install
go install github.com/voocel/codebot/cmd/codebot@latest

# Set API key and run
export ANTHROPIC_API_KEY=sk-ant-...
codebot
```

Or build from source:

```bash
git clone https://github.com/voocel/codebot.git
cd codebot && go build -o codebot ./cmd/codebot
```

Supported environment variables: `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `GEMINI_API_KEY`. For more options see [settings.example.jsonc](settings.example.jsonc).

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

## Configuration

Config files: `~/.codebot/settings.json` (global) or `.codebot/settings.json` (project-level, takes precedence).

All fields are optional. See [settings.example.jsonc](settings.example.jsonc) for the full reference with comments.

## Requirements

- Go 1.25+
- API key for at least one provider

## License

MIT