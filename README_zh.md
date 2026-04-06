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
- `codebot` 是 Harness / 运行时层：Prompt 组装、会话持久化、审批流、上下文压缩、运行时提醒、TUI 编排

这个分层很重要。Agent 循环保持小而可复用，而长周期终端工作流中的复杂性放在 Harness 层解决。

## 功能

**Agent**
- 流式响应，支持扩展思考（off → xhigh）
- 工具执行：read, write, edit, bash, grep, find, ls, web_search, web_fetch
- 任务管理：task_create, task_get, task_update, task_list（SubAgent 协调）
- SubAgent 委托，支持并行/链式执行
- 上下文满时自动压缩
- 多 Provider：Anthropic、OpenAI、OpenRouter、Gemini
- MCP（Model Context Protocol）服务器集成

**会话**
- 仅追加 JSONL 持久化 — 崩溃安全、人类可读
- 恢复会话（`-c` 最近，`-r` 选择），支持分叉、回放
- 模型和思考级别按会话保存

**安全**
- 四种权限模式：`strict` / `balanced` / `accept-edits` / `trust`
- 危险命令拦截（rm -rf, sudo, dd, ...）
- 工作区范围的文件访问控制
- 每次工具决策的 JSON 审计日志

**界面**
- 交互式 TUI，实时流式输出和 Markdown 渲染
- Plan 模式：Agent 提出修改方案，用户审核批准
- AskUser：Agent 向用户发起结构化多选问题
- 图片粘贴（Ctrl+V），支持选择（↑）和删除（Delete）
- 任务进度展示：进度条 + 状态图标，固定在输入区上方
- 非交互管道模式（`-p`）
- 斜杠命令：`/model`, `/compact`, `/plan`, `/resume`, `/copy`, ...

**扩展**
- plugin-first 架构，支持 project / user 两级 plugin
- plugin 贡献：skills、commands、MCP servers
- `/plugins create`、`/plugins install`、`/plugins remove` 管理本地 plugin 生命周期
- trust / enable / disable 治理与运行时热刷新

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
# 设置 API Key 并运行
export ANTHROPIC_API_KEY=sk-ant-...
codebot
```

支持的环境变量：`ANTHROPIC_API_KEY`、`OPENAI_API_KEY`、`OPENROUTER_API_KEY`、`GEMINI_API_KEY`。更多配置项参考 [settings.example.jsonc](settings.example.jsonc)。

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
- **Harness 层（`codebot`）**：会话控制、运行时策略、审批路由、Prompt 组装、上下文工程、恢复流程、交互体验
- **应用表层**：TUI、print 模式、slash 命令、会话恢复/分叉、配置

这意味着 codebot 不只是“带工具的 Agent”，而是“Agent + 面向长周期终端工作流的 Harness”。

## 配置

配置文件：`~/.codebot/settings.json`（全局）或 `.codebot/settings.json`（项目级，优先）。

所有字段可选，参考 [settings.example.jsonc](settings.example.jsonc) 了解完整配置项及说明。

Plugin 开发参考 [docs/plugins.md](docs/plugins.md)。真实示例 plugin 放在 `docs/examples/plugins/`，目前包含 `review-assistant`、`release-ops`、`docs-context`。

## 环境要求

- 至少一个 Provider 的 API Key
- Go 1.25+（仅通过 `go install` 或源码构建时需要）

## 许可证

MIT
