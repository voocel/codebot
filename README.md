# Codebot

[English](README.md) | [中文](README_zh.md)

Terminal-native AI coding agent. Built on [agentcore](https://github.com/voocel/agentcore), a minimal agent execution kernel.

<p align="center">
  <img src="scripts/sample.gif" alt="Codebot Demo" width="800">
</p>

## Why

Most AI coding tools are either bloated frameworks or thin API wrappers. Codebot sits in between: a **complete agent** with session management, security policies, and a polished TUI.

The trick: **agentcore handles execution, codebot handles coordination.**

Each layer has one job. No layer knows about the layers above it.

In architecture terms, codebot now acts as a **terminal-native harness** on top of `agentcore`:

- `agentcore` is the execution kernel: agent loop, tools, events, message state
- `codebot` is the harness/runtime layer: prompt composition, session persistence, approval flow, and the TUI, print, and ACP frontends

This split matters. The agent loop stays small and reusable, while long-running terminal concerns live in the harness where they belong.

## Features

**Agent**
- Streaming responses with configurable reasoning effort (off → xhigh)
- Tool execution: read, write, edit, bash, grep, glob, ls, web_search, web_fetch
- Todo list: `todo_write` keeps a visible checklist for multi-step work
- SubAgent delegation with parallel/chain execution
- Automatic context compaction when window fills up
- Multi-provider: Anthropic, OpenAI, OpenRouter, Gemini, DeepSeek
- MCP (Model Context Protocol) server integration

**Sessions**
- Append-only JSONL persistence — crash-safe, human-readable
- Resume (`-c` last, `-r` pick)
- Model and reasoning effort restored per session
- Sessions saved by versions before the session-format change (format 3) can't be resumed and are left out of `-r`; delete them from `~/.codebot/projects/*/` if you no longer need them

**Security**
- Four permission modes: `strict` / `balanced` / `accept-edits` / `trust` (Shift+Tab cycles)
- Destructive commands (rm -rf, git reset --hard, ...) flagged in the approval prompt
- Touching credentials, or changing shell startup files or git hooks, is confirmed every time, in every mode
- Workspace trust: a repository's hooks, MCP servers, plugins and allow rules stay off until you trust the folder, and you are asked again when they change
- Workspace-scoped file access
- JSON audit log for every tool decision

**Interface**
- Interactive TUI with real-time streaming and markdown rendering
- AskUser: structured multi-choice questions from agent to user
- Image paste (Ctrl+V) with selection (↑) and deletion (Delete)
- Todo list shown above the input
- Non-interactive print mode for pipes and scripts (`-p`)
- Slash commands: `/model`, `/compact`, `/resume`, `/copy`, ...; every skill is also a `/` command

**Extensibility**
- Skills (Agent Skills `SKILL.md`), sub-agents, MCP servers and hooks, from you and from the project
- Plugins in the [Agent Plugins](https://github.com/agentplugins/agent-plugins-spec) format bundle skills and MCP servers, from git or a directory, locked at the commit you agreed to
- Reads `.agents/skills`, shared with other coding agents
- Custom slash commands are skills: add `disable-model-invocation: true` to keep one user-only
- `/reload` picks up changes; `/status` shows where each extension comes from

## Installation

**Pre-built binary (recommended):**

```bash
# Linux / macOS
curl -fsSL https://raw.githubusercontent.com/voocel/codebot/main/scripts/install.sh | sh

# Windows (PowerShell)
irm https://raw.githubusercontent.com/voocel/codebot/main/scripts/install.ps1 | iex
```

Or download directly from [GitHub Releases](https://github.com/voocel/codebot/releases).

**With Go:**

```bash
go install github.com/voocel/codebot/cmd/codebot@latest
```

**Build from source:**

```bash
git clone https://github.com/voocel/codebot.git
cd codebot && go build -o codebot ./cmd/codebot
```

## Quick Start

```bash
codebot
```

The first run launches a setup wizard: pick a provider, type a model id, paste your API key. Everything lands in `~/.codebot/settings.json` — the single source of configuration — and can be re-run anytime with `codebot -setup`. For more options see [settings.example.jsonc](settings.example.jsonc).

OpenRouter can be used as a first-class provider in `settings.json`:

```json
{
  "provider": "openrouter",
  "model": "openai/gpt-5",
  "providers": {
    "openrouter": {
      "api_key": "sk-or-...",
      "base_url": "https://openrouter.ai/api/v1"
    }
  }
}
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
codebot --mode strict
```

## Design Principles

1. **Reuse before reinvent** — agentcore does the agent loop, codebot doesn't redo it
2. **No premature abstraction** — every interface has at least two real callers
3. **Convention over configuration** — sensible defaults, explicit overrides
4. **Secure by default** — balanced mode, audit trail, workspace boundaries

## Architecture

Codebot follows a layered coding-agent architecture:

- **Execution kernel (`agentcore`)**: model calls, tool execution, event stream, message lifecycle
- **Harness layer (`codebot`)**: session control, approval routing, prompt assembly, hooks, and UX
- **Application surface**: TUI, print mode, ACP, slash commands, session resume, configuration

This means codebot is not just "an agent with tools". It is an agent plus a harness for long-running terminal workflows.

## Configuration

Config files: `~/.codebot/settings.json` (yours) and `.codebot/settings.json` at the project's root, the top of its git repository. The project's fields take precedence over yours, except those it may not set: `providers`, `search_provider`, `search_api_key` and `telemetry` are yours alone, so a repository can never send your keys or your conversations elsewhere. `/model` saves its choice to your file.

All fields are optional. See [settings.example.jsonc](settings.example.jsonc) for the full reference with comments.

Provider entries support `extra` for connection settings sent as HTTP/client config, never as request-body fields: `user_agent`, `headers`, and `anthropic_beta` (an explicit `anthropic-beta` header wins); Bedrock takes `region`, `access_key_id`, `secret_access_key`, and optionally `session_token` instead of `api_key`.

A provider of type `anthropic` loads tools on demand through tool search, which takes Claude 4.5 or later; older Claude models, and other vendors' models behind an Anthropic-compatible endpoint, may reject such requests.

For Claude on Amazon Bedrock, use Bedrock's Messages API rather than Converse: `type: "anthropic"`, `base_url: "https://bedrock-mantle.<region>.api.aws/anthropic"`, a Bedrock API key as `api_key`, and model IDs such as `anthropic.claude-opus-5-5`. It takes Anthropic's request format, so tools load on demand through tool search as with Anthropic. Converse cannot defer tools: a tool that joins mid-conversation, such as from an MCP server that connected late, changes the request, which restarts the prompt cache and can get the request rejected by Claude models that bind their thinking to it (Fable 5.1, Opus 5.5, Sonnet 5.5).

Context windows, output caps, and prices come from LiteLLM's model list: a snapshot built into codebot, refreshed daily into `~/.codebot/litellm-models.json`.

OpenAI-protocol providers also support `api: "chat"` (default) or `api: "responses"` to choose between `/v1/chat/completions` and `/v1/responses`.

A provider of `type: "gateway"` is a [LiteLLM gateway](https://github.com/voocel/litellm#gateway): codebot runs the agent, say in a sandbox, while the gateway at `base_url` holds the provider keys, makes the model calls and bills them; `api_key` is codebot's token for the gateway. Reasoning effort, prompt caching and retries work as with a direct provider.

## Extensions

| | Yours | The project's |
|---|---|---|
| Skills | `~/.codebot/skills/`, `~/.agents/skills/` | `.codebot/skills/` at the root, `.agents/skills/` from the working directory up to the root |
| Sub-agents | `~/.codebot/agents/*.md` | `.codebot/agents/*.md` at the root |
| MCP servers, hooks, plugins | `~/.codebot/settings.json` | `.codebot/settings.json` at the root |

A skill is a directory holding a `SKILL.md`, or a single `.md` file. Of two of one name, the project's wins over yours, and yours over a built-in one; `/status` lists what each replaced. Hooks replace none: yours and the project's all run.

**Plugins.** A plugin bundles skills and MCP servers in the [Agent Plugins 1.0](https://github.com/agentplugins/agent-plugins-spec) format: a directory with a `plugin.json` naming it, skills in `skills/<name>/SKILL.md` and MCP servers in `mcp.json`. Its skills become `/<plugin>:<skill>`, its MCP servers `<plugin>_<server>`. `plugins` in the settings lists where they come from: a git repository (`github.com/acme/tools`, an https or ssh URL, with `#ref` for a branch, tag or commit), or a directory, relative to the settings file naming it.

- `/plugins add <source> [--project]` fetches a plugin, shows what it would run, and once you agree adds it to your settings, or the project's. A relative path is taken from where you are. A plugin your settings declare that you never added is not installed until you add it this way.
- `/plugins update [name]` fetches git plugins anew at their ref. An update that runs nothing new applies at once; one that does waits for you to agree to what it adds.
- `/plugins remove <name>` removes one. `/plugins` lists them; space turns one off in this project, for you alone.

A git plugin is fetched into `~/.codebot/plugins/cache/` and locked at that commit in `~/.codebot/plugins/lock.json`: it changes only when you update it. One no longer cached is fetched again at that commit. Its MCP servers keep their data in `~/.codebot/plugins/data/<plugin>/` (`${PLUGIN_DATA}`), which updates leave alone. A project's plugins wait for you to trust the folder; once trusted, those not fetched yet are fetched, and codebot asks about what they run before it runs. Writes into a local plugin's directory are confirmed every time. Of two plugins of one name, the project's wins; an MCP server in the settings wins over a plugin's of the same name.

**Workspace trust.** A repository may come from anyone, so what in it runs code or lets calls through unasked takes effect only once you trust the folder: its hooks, MCP servers, plugins, allow rules, read and write roots, and the commands, allowed tools and model its skills declare. Its skills and sub-agents themselves load either way; they are instructions for the model. A project's skills are read as they load, so what runs is what you trusted; `/reload` picks up their edits. The first time, codebot lists all of it and asks; once trusted, it asks again only about what was added. `/trust` shows and changes the decision, which is kept in `~/.codebot/workspaces.json`, never in the repository. Print mode and ACP have nobody to ask: an undecided folder stays untrusted, they say on stderr what is off, and `--trust` trusts it for that run.

## Requirements

- API key for at least one provider
- Go 1.26+ (only if installing via `go install` or building from source)

## License

[Apache License 2.0](LICENSE)
