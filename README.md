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
- Workspace trust: a repository's hooks, MCP servers, plugins and allow rules stay off until you trust them, item by item: what it adds later waits for you while the rest runs on
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
- Plugins in the [Agent Plugins](https://github.com/agentplugins/agent-plugins-spec) format bundle skills, MCP servers, hooks and sub-agents, from git or a directory; one runs once you agree to it as a whole, and a git one stays at the commit you agreed to
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

The first run launches a setup wizard: pick a provider, paste your API key (one already in its usual environment variable, such as `ANTHROPIC_API_KEY`, is filled in), then pick a model from those the key reaches — listing them checks the key. Ollama takes no key, and any other endpoint takes a name, a protocol and a base URL. Everything lands in `~/.codebot/settings.json` — the single source of configuration — and can be re-run anytime with `codebot -setup`, which starts from your current provider and its saved key. For more options see [settings.example.jsonc](settings.example.jsonc).

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

A skill is a directory holding a `SKILL.md`, or a single `.md` file. Of two of one name, the project's wins over yours, and yours over a built-in one; `/status` lists what each replaced. Hooks replace none: yours and the project's all run. A hook with an unknown event, type or field is reported and left out. A command hook runs in `sh`; on Windows, its `command_windows`, if it has one, runs in PowerShell instead, and its `command` alone needs an `sh` on `PATH`, such as Git for Windows brings, or the hook is reported as it loads. PowerShell turns an exit code other than 0 or 1 into 1, so a Windows command that blocks by exiting 2 ends with `exit $LASTEXITCODE`, or prints `{"block": true}`.

**MCP servers over HTTP.** An HTTP server you gave no `Authorization` header signs in with OAuth, as the MCP specification lays out: when it asks for authorization, codebot says so, and `/mcp login <server>` opens the browser to sign in. codebot introduces itself to the server's authorization server with its [client metadata document](site/oauth/client.json). An authorization server that takes none, such as GitHub's, wants an OAuth app you register with it, its callback URL `http://127.0.0.1/callback`, and the app's client ID and secret as the server's `oauth`, the secret taken from your environment if you write it as `${VAR}` (see `settings.example.jsonc`). codebot keeps the tokens in `~/.codebot/mcp-oauth.json`, readable by you alone, and refreshes them; the model is asked every time before it reads or writes that file. `/mcp logout <server>` forgets the token. A server with an `Authorization` header of yours uses that header instead.

**Plugins.** A plugin bundles skills and MCP servers in the [Agent Plugins 1.0](https://github.com/agentplugins/agent-plugins-spec) format: a directory with a `plugin.json` naming it, skills in `skills/<name>/SKILL.md` and MCP servers in `mcp.json`. Its skills become `/<plugin>:<skill>`, its MCP servers `<plugin>_<server>`. Hooks and sub-agents are beyond that format, so codebot reads them from its own namespace, `io.github.voocel.codebot`: sub-agents from its directory, `io.github.voocel.codebot/agents/`, and hooks from its entry in `plugin.json`:

```json
"extensions": { "io.github.voocel.codebot": {
  "hooks": { "PreToolUse": [{ "matcher": "bash", "type": "command", "command": "\"$PLUGIN_ROOT\"/guard",
    "command_windows": "& \"$env:PLUGIN_ROOT\\guard.exe\"; exit $LASTEXITCODE" }] }
} }
```

`hooks` takes hooks as the settings do; they run with `PLUGIN_ROOT` and `PLUGIN_DATA` in their environment, which PowerShell reads as `$env:PLUGIN_ROOT`. The sub-agents are written as in `.codebot/agents/` and become `<plugin>:<agent>`.

`plugins` in the settings lists where plugins come from: a git repository (`github.com/acme/tools`, an https or ssh URL, with `//dir` for a plugin in a directory of it and `#ref` for a branch, tag or commit, as in `github.com/acme/plugins//tools#main`), or a directory, relative to the settings file naming it. `--plugin-dir <dir>`, repeatable, loads a plugin for one run, all it does in effect: for one you are writing.

- `/plugins add <source> [--project]` fetches a plugin, shows what it would run, and adds it to your settings, or the project's, if you take it as a whole. A relative path is taken from where you are; one in the project goes into its settings relative to them, so it holds wherever the project is checked out.
- `/plugins install` readies what the settings declare and waits for you: a git plugin you have yet to agree to is fetched and shown, as is one that runs more than you agreed to; one you agreed to that is no longer cached is fetched again at its commit. codebot tells you as it starts when any wait.
- `/plugins update [name]` fetches git plugins anew at their ref, also when you changed the ref in the settings. An update that runs nothing new applies at once; one that does stays on the old commit until you agree to what it adds. As it starts, the TUI asks the remotes of the git plugins in effect where their ref is, fetching nothing, and tells which have moved on.
- `/plugins remove <name> [--project]` removes one from your settings, or the project's; your agreement to it, and its data, stay, as another project may declare it. `/plugins` lists them; enter shows what one brings and runs.

What a plugin runs is its hooks and MCP servers, and the commands, allowed tools and model its skills declare; its skills and sub-agents themselves are instructions for the model. Its author tested it as one, so you agree to it as a whole, by its source without the ref, in `~/.codebot/consent.json`: a git plugin at a commit, a local one as it is now. A package runner given no exact version, such as `npx -y some-mcp`, runs the newest release each time, which no agreement pins; the panel says so. Nothing is fetched or put in effect but as you agree: a git plugin you have yet to agree to is not installed, and a plugin that runs something you have yet to agree to, such as a local one you edited, is off entirely until you do. A plugin you would rather not have, you remove. A git plugin is fetched into `~/.codebot/plugins/cache/` and changes only when you update it; a commit no session read for two weeks is removed, and fetched again at that commit when needed. What a plugin runs keeps its data in a directory of `~/.codebot/plugins/data/` of its source's own (`${PLUGIN_DATA}`, shown in `/plugins`), which updates leave alone and codebot never removes. A project's plugins wait for you to trust the project to declare them, then for you to agree to what they run, as yours do. Writes into a local plugin's directory are confirmed every time. Of two plugins of one name, one given with `--plugin-dir` wins, then the project's; an MCP server in the settings wins over a plugin's of the same name.

**Workspace trust.** A repository may come from anyone, so what in it runs code or lets calls through unasked takes effect only once you trust it to: each of its hooks, MCP servers, plugins, allow rules, read and write roots, and the commands, allowed tools and model its skills declare. Its skills and sub-agents themselves load either way; they are instructions for the model, and a skill whose commands you have yet to trust loads without them. Trust is item by item: the panel lists each item checked, and you uncheck what you would not run. What you declined stays off, unasked; what the folder adds once you trust it stays off until you decide on that too, while the rest runs on, and codebot asks about it once a session. `/trust` shows all of it and changes the decisions, kept in `~/.codebot/consent.json`, never in the repository; "Don't trust this folder" there keeps it all off without asking again. A project's MCP servers get no `${VAR}` from your environment, and its settings, skills, sub-agents and `AGENTS.md` are read only inside it, an `AGENTS.md` above it only inside its own directory: a link leading out is left out. What shows on a panel is what runs: text a terminal would act on, or that could read as other text, shows quoted. A project's skills are read as they load, so what runs is what you trusted; `/reload` picks up their edits and restarts the MCP servers. Print mode and ACP have nobody to ask: what you have yet to trust stays off, they say on stderr what, and `--trust` trusts the folder, with the plugins it declares, for that run.

## Requirements

- API key for at least one provider
- Go 1.26+ (only if installing via `go install` or building from source)

## License

[Apache License 2.0](LICENSE)
