# Codebot

[English](README.md) | [中文](README_zh.md)

终端原生 AI 编程助手。基于 [agentcore](https://github.com/voocel/agentcore) 构建，一个极简的 Agent 执行内核。

<p align="center">
  <img src="scripts/sample.gif" alt="Codebot Demo" width="800">
</p>

## 为什么

大多数 AI 编程工具要么是臃肿的框架，要么是薄薄的 API 封装。Codebot 介于两者之间：一个**完整的 Agent**，具备会话管理、安全策略和精致的 TUI。

核心思路：**agentcore 负责执行，codebot 负责编排。**

每一层只做一件事，下层不感知上层。

从架构上说，codebot 现在已经形成了清晰的 **Harness 层**，建立在 `agentcore` 之上：

- `agentcore` 是执行内核：Agent 循环、工具调用、事件流、消息状态
- `codebot` 是 Harness / 运行时层：Prompt 组装、会话持久化、审批流，以及 TUI、print、ACP 三个前端

这个分层很重要。Agent 循环保持小而可复用，而长周期终端工作流中的复杂性放在 Harness 层解决。

## 功能

**Agent**
- 流式响应，支持可配置推理强度（off → xhigh）
- 工具执行：read, write, edit, bash, grep, glob, ls, web_search, web_fetch
- Todo 清单：`todo_write` 为多步任务维护可见的清单
- SubAgent 委托，支持并行/链式执行
- 上下文满时自动压缩
- 多 Provider：Anthropic、OpenAI、OpenRouter、Gemini、DeepSeek
- MCP（Model Context Protocol）服务器集成

**会话**
- 仅追加 JSONL 持久化 — 崩溃安全、人类可读
- 恢复会话（`-c` 最近，`-r` 选择）
- 模型和推理强度按会话保存
- 会话格式变更之前（格式 3）保存的会话无法恢复，也不会出现在 `-r` 列表里；不再需要的话可从 `~/.codebot/projects/*/` 删除

**安全**
- 四种权限模式：`strict` / `balanced` / `accept-edits` / `trust`（Shift+Tab 切换）
- 破坏性命令（rm -rf、git reset --hard 等）在审批提示中标出
- 读写凭据、修改 shell 启动文件或 git hooks，在任何模式下每次都要确认
- 工作区信任：仓库里的 hooks、MCP 服务器、插件和 allow 规则，要等你逐项信任后才生效；后来新增的等你确认，其余照常运行
- 工作区范围的文件访问控制
- 每次工具决策的 JSON 审计日志

**界面**
- 交互式 TUI，实时流式输出和 Markdown 渲染
- AskUser：Agent 向用户发起结构化多选问题
- 图片粘贴（Ctrl+V），支持选择（↑）和删除（Delete）
- Todo 清单固定在输入区上方
- 非交互管道模式（`-p`）
- 斜杠命令：`/model`, `/compact`, `/resume`, `/copy`, ...；每个 skill 也是一个 `/` 命令

**扩展**
- Skills（Agent Skills 的 `SKILL.md`）、子 agent、MCP 服务器、hooks，来自你自己，也可以来自项目
- [Agent Plugins](https://github.com/agentplugins/agent-plugins-spec) 格式的插件，把 skills、MCP 服务器、hooks 和子 agent 打包在一起，来自 git 或本地目录；插件要你整体同意后才生效，git 插件固定在你同意过的 commit
- 读取 `.agents/skills`，和其他 coding agent 共用
- 自定义斜杠命令就是 skill：加 `disable-model-invocation: true` 即只供用户调用
- `/reload` 重新加载改动；`/status` 显示每项扩展的来源

## 安装

**预编译二进制（推荐）：**

```bash
# Linux / macOS
curl -fsSL https://raw.githubusercontent.com/voocel/codebot/main/scripts/install.sh | sh

# Windows (PowerShell)
irm https://raw.githubusercontent.com/voocel/codebot/main/scripts/install.ps1 | iex
```

或直接从 [GitHub Releases](https://github.com/voocel/codebot/releases) 下载。

**通过 Go 安装：**

```bash
go install github.com/voocel/codebot/cmd/codebot@latest
```

**从源码构建：**

```bash
git clone https://github.com/voocel/codebot.git
cd codebot && go build -o codebot ./cmd/codebot
```

## 快速开始

```bash
codebot
```

首次运行会进入配置向导：选择 provider、填写模型 id、粘贴 API Key，全部保存到 `~/.codebot/settings.json`（配置的唯一来源），之后可随时用 `codebot -setup` 重新配置。更多配置项参考 [settings.example.jsonc](settings.example.jsonc)。

OpenRouter 也可以作为一等 provider 使用，在 `settings.json` 中这样配置：

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

## 使用

```bash
# 交互式 TUI
codebot

# 管道模式
echo "explain main.go" | codebot -p

# 继续上次会话
codebot -c

# 严格安全策略
codebot --mode strict
```

## 设计原则

1. **复用优先** — agentcore 做 Agent 循环，codebot 不重复造轮子
2. **拒绝过早抽象** — 每个接口至少有两个真实调用者
3. **约定优于配置** — 合理默认值，显式覆盖
4. **默认安全** — balanced 模式、审计追踪、工作区边界

## 架构说明

Codebot 采用分层的 Coding Agent 架构：

- **执行内核（`agentcore`）**：模型调用、工具执行、事件流、消息生命周期
- **Harness 层（`codebot`）**：会话控制、审批路由、Prompt 组装、hooks、交互体验
- **应用表层**：TUI、print 模式、ACP、slash 命令、会话恢复、配置

这意味着 codebot 不只是“带工具的 Agent”，而是“Agent + 面向长周期终端工作流的 Harness”。

## 配置

配置文件：`~/.codebot/settings.json`（你自己的）和项目根目录（所在 git 仓库的顶层）下的 `.codebot/settings.json`。项目的字段优先于你的，但有几项项目不能设置：`providers`、`search_provider`、`search_api_key` 和 `telemetry` 只认你自己的，所以仓库永远没法把你的 key 或对话发到别处。`/model` 的选择保存在你自己的文件里。

所有字段可选，参考 [settings.example.jsonc](settings.example.jsonc) 了解完整配置项及说明。

Provider 条目支持 `extra`，用于配置连接参数，作为 HTTP/客户端配置发送，不会进入请求体：`user_agent`、`headers`、`anthropic_beta`（`headers` 中显式的 `anthropic-beta` 优先）；Bedrock 不用 `api_key`，改用 `region`、`access_key_id`、`secret_access_key`，可选 `session_token`。

`type` 为 `anthropic` 的 provider 通过工具搜索按需加载工具，这需要 Claude 4.5 或更新的模型；更早的 Claude 模型，以及通过 Anthropic 兼容端点接入的其他厂商模型，可能会拒绝这类请求。

在 Amazon Bedrock 上用 Claude 时，请走 Bedrock 的 Messages API，而不是 Converse：`type: "anthropic"`，`base_url: "https://bedrock-mantle.<region>.api.aws/anthropic"`，`api_key` 填 Bedrock API key，模型 ID 形如 `anthropic.claude-opus-5-5`。它使用 Anthropic 的请求格式，所以和直连 Anthropic 一样，工具通过工具搜索按需加载。Converse 不能延迟加载工具：对话中途加入的工具（比如晚连上的 MCP 服务器提供的）会改动请求，提示缓存会重新开始；对把思考内容绑定在请求前缀上的 Claude 模型（Fable 5.1、Opus 5.5、Sonnet 5.5），请求还可能被拒绝。

上下文窗口、输出上限和价格来自 LiteLLM 的模型列表：codebot 内置一份快照，每天刷新到 `~/.codebot/litellm-models.json`。

OpenAI 协议 provider 还支持 `api: "chat"`（默认）或 `api: "responses"`，用于在 `/v1/chat/completions` 和 `/v1/responses` 之间切换。

`type: "gateway"` 的 provider 是一个 [LiteLLM 网关](https://github.com/voocel/litellm/blob/main/README_CN.md#网关)：codebot 负责运行 agent（比如在沙盒里），`base_url` 处的网关持有 provider key、执行模型调用并计费；`api_key` 是 codebot 访问网关的 token。思考级别、prompt 缓存和重试与直连 provider 时一样有效。

## 扩展

| | 你自己的 | 项目的 |
|---|---|---|
| Skills | `~/.codebot/skills/`、`~/.agents/skills/` | 根目录下的 `.codebot/skills/`；从工作目录逐级到根目录的 `.agents/skills/` |
| 子 agent | `~/.codebot/agents/*.md` | 根目录下的 `.codebot/agents/*.md` |
| MCP 服务器、hooks、插件 | `~/.codebot/settings.json` | 根目录下的 `.codebot/settings.json` |

一个 skill 是一个含 `SKILL.md` 的目录，或者一个 `.md` 文件。同名时项目的优先于你的，你的优先于内置的；`/status` 会列出谁覆盖了谁。hooks 不互相覆盖，你的和项目的都会运行。事件、类型或字段不认识的 hook 会报出来，并且不加载。command hook 用 `sh` 运行；在 Windows 上，有 `command_windows` 就改用 PowerShell 运行它，只有 `command` 的需要 `PATH` 上有 `sh`（比如 Git for Windows 带的），否则加载时就报出来。PowerShell 会把 0 和 1 以外的退出码都变成 1，所以 Windows 命令要靠退出码 2 拦截，就以 `exit $LASTEXITCODE` 结尾，或者输出 `{"block": true}`。

**HTTP 上的 MCP 服务器。** 没有配置 `Authorization` 头的 HTTP 服务器按 MCP 规范用 OAuth 登录：服务器要求授权时 codebot 会提示，`/mcp login <server>` 打开浏览器完成登录。codebot 用自己的 [client metadata document](site/oauth/client.json) 向该服务器的授权服务器表明身份。不接受这种文档的授权服务器（比如 GitHub 的）需要你在那里注册一个 OAuth 应用，回调 URL 填 `http://127.0.0.1/callback`，再把它的 client ID 和 secret 配成该服务器的 `oauth`，secret 可以写成 `${VAR}` 从环境变量读取（见 `settings.example.jsonc`）。token 保存在只有你能读的 `~/.codebot/mcp-oauth.json` 里并自动刷新；模型每次读写这个文件都要你确认。`/mcp logout <server>` 删除 token。配置了自己的 `Authorization` 头的服务器照旧使用这个头。

**插件。** 插件按 [Agent Plugins 1.0](https://github.com/agentplugins/agent-plugins-spec) 格式把 skills 和 MCP 服务器打包在一起：一个目录，`plugin.json` 给出插件名，skills 放在 `skills/<name>/SKILL.md`，MCP 服务器写在 `mcp.json`。它的 skill 叫 `/<plugin>:<skill>`，MCP 服务器叫 `<plugin>_<server>`。这个格式不含 hooks 和子 agent，codebot 从 `plugin.json` 里自己的命名空间读取：

```json
"extensions": { "io.github.voocel.codebot": {
  "hooks": { "PreToolUse": [{ "matcher": "bash", "type": "command", "command": "\"$PLUGIN_ROOT\"/guard",
    "command_windows": "& \"$env:PLUGIN_ROOT\\guard.exe\"; exit $LASTEXITCODE" }] },
  "agents": "./agents"
} }
```

`hooks` 的写法和设置里的 hooks 相同，运行时环境变量里有 `PLUGIN_ROOT` 和 `PLUGIN_DATA`，PowerShell 里写作 `$env:PLUGIN_ROOT`。`agents` 指向一个子 agent 目录，格式和 `.codebot/agents/` 相同，名字是 `<plugin>:<agent>`。

设置里的 `plugins` 列出插件来源：git 仓库（`github.com/acme/tools`、https 或 ssh 地址，可加 `//目录` 指定仓库里的插件目录，加 `#ref` 指定分支、tag 或 commit，比如 `github.com/acme/plugins//tools#main`），或者一个目录，相对于声明它的设置文件。`--plugin-dir <目录>` 可重复使用，只为这一次运行加载插件，它的全部内容直接生效，适合开发插件时用。

- `/plugins add <source> [--project]` 拉取插件，列出它会运行的东西，你整体接受后写进你的设置或项目的设置。相对路径从当前目录算起；项目里的目录写进项目设置时换算成相对路径，项目在哪里检出都能用。
- `/plugins install` 处理设置里声明了、还在等你的插件：你还没同意过的 git 插件，拉取后列出来让你确认；运行了你没同意过的东西的插件，也一样；同意过但缓存丢了的，按同意过的 commit 重新拉取。有插件在等你时，启动时会提示。
- `/plugins update [name]` 按 ref 重新拉取 git 插件，你在设置里改了 ref 也用它。没有新增可执行内容的更新直接生效；有新增的，在你同意新增部分之前留在旧 commit。TUI 启动时会向生效中 git 插件的远端查询 ref 指向的提交（不拉取内容），告诉你哪些有更新。
- `/plugins remove <name> [--project]` 从你的设置或项目的设置里移除插件；你对它的同意和它的数据都保留，因为别的项目可能还声明着它。`/plugins` 列出所有插件，回车查看它带来和要运行的东西。

插件要运行的东西，是它的 hooks、MCP 服务器，以及它的 skills 声明的命令、预授权工具和模型；skills 和子 agent 本身只是给模型的指令。插件是作者当作一个整体测试的，所以你整体同意，按不带 ref 的来源记在 `~/.codebot/consent.json`：git 插件同意的是某个 commit，本地插件同意的是它现在的内容。没有你的同意，什么都不会拉取或生效：你还没同意过的 git 插件显示为未安装；运行了你没同意过的东西的插件（比如你改过的本地插件）整个不生效，直到你同意。不想要的插件就移除它。git 插件拉取到 `~/.codebot/plugins/cache/`，只有你更新时才会变；两周没有会话读过的 commit 会被删除，需要时再按那个 commit 重新拉取。插件运行的东西把数据放在 `~/.codebot/plugins/data/` 下按来源区分的目录里（`${PLUGIN_DATA}`，`/plugins` 里能看到），更新不会动它，codebot 也从不删除它。项目的插件要先等你信任项目声明它，再像你自己的插件一样，等你同意它要运行的东西。往本地插件目录里写文件，每次都要确认。两个插件同名时，`--plugin-dir` 给的优先，其次是项目的；设置里的 MCP 服务器和插件的同名时，设置里的优先。

**工作区信任。** 仓库可能来自任何人，所以其中会执行代码、或者能免确认放行调用的东西，要等你信任后才生效：它的每个 hook、MCP 服务器、插件、allow 规则、读写目录，以及它的 skills 声明的命令、预授权工具和模型。它的 skills 和子 agent 本身照常加载，那只是给模型的指令；命令还没被信任的 skill，加载时不带这些命令。信任是逐项的：面板默认勾选每一项，不想运行的取消勾选即可。拒绝的保持关闭，不会再问；信任之后目录新增的内容，要等你决定才生效，其余照常运行；同一个会话里只问一次。`/trust` 列出全部内容并修改这些决定，决定保存在 `~/.codebot/consent.json`，不会写进仓库；在那里选“不信任这个目录”后全部不生效，也不会再问。项目的 MCP 服务器拿不到你环境里的 `${VAR}`；项目的设置、skills、子 agent 和 `AGENTS.md` 只在项目内读取，项目以上目录的 `AGENTS.md` 只在它自己的目录内读取，指向外面的链接会被排除。面板上显示的就是实际运行的：终端会执行的字符、或可能被看成别的文本的内容，会加引号转义显示。项目的 skill 在加载时读取一次，所以执行的就是你信任过的内容；改了要 `/reload` 才生效，`/reload` 也会重启 MCP 服务器。print 模式和 ACP 没有人可问：还没信任的内容不生效，并在 stderr 说明哪些没生效；加 `--trust` 只在这一次运行里信任这个目录和它声明的插件。

## 环境要求

- 至少一个 Provider 的 API Key
- Go 1.26+（仅通过 `go install` 或源码构建时需要）

## 许可证

[Apache License 2.0](LICENSE)
