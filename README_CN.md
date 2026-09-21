# CLIProxyAPI

[English](README.md) | 中文 | [日本語](README_JA.md)

CLIProxyAPI 是一个代理服务器，可在你已有的 CLI 工具与订阅之上，暴露 **OpenAI、Gemini、Claude、Codex、Grok 兼容的 API**。在本地（或服务器）运行它，登录一次提供方账号，然后让任意兼容的客户端或 SDK 指向同一个端点即可。CLIProxyAPI 会负责协议转换、账号轮询、负载均衡、流式响应、工具调用，并在提供方支持时处理 WebSocket。

它适合以下场景：

- 通过任意 OpenAI、Anthropic 或 Gemini 兼容的客户端或 SDK 使用 Claude Code、Codex CLI、Gemini CLI 等 CLI 工具。
- 汇总同一提供方的多个账号，并以轮询方式分摊请求。
- 复用已有的 CLI 订阅，而无需手动管理原始 API Key。
- 通过可复用的 SDK 将多提供方网关嵌入到自己的 Go 应用中。

## 目录

- [功能特性](#功能特性)
- [支持的提供方](#支持的提供方)
- [快速开始](#快速开始)
- [配置](#配置)
- [使用 API](#使用-api)
- [管理 API](#管理-api)
- [Docker](#docker)
- [Go SDK](#go-sdk)
- [使用量统计](#使用量统计)
- [贡献](#贡献)
- [社区项目](#社区项目)
- [许可证](#许可证)

## 功能特性

- 为 CLI 模型提供 OpenAI、Gemini、Claude、Codex、Grok 兼容的 API 端点。
- 支持 OAuth 登录（Codex、Claude Code、Antigravity/Gemini、Grok、Kimi、Devin、Meta）。
- 支持 API Key 接入 Gemini AI Studio、Codex、Claude、xAI、Meta 和 Vertex。
- 在支持场景下提供流式、非流式以及 WebSocket 响应。
- 支持函数调用 / 工具调用以及多模态输入（文本与图片）。
- 同一提供方支持多账号轮询负载均衡与自动故障转移。
- 为每个 OAuth 提供方提供简单的 CLI 认证流程。
- 通过 YAML 配置接入 OpenAI 兼容的上游提供方（例如 OpenRouter）。
- 支持请求重试、凭据冷却以及按提供方覆盖配置。
- 提供管理 API 以及可选的 Web 控制面板。
- 支持 mDNS / DNS-SD 局域网服务发现。
- 支持插件扩展提供方行为。
- 提供可复用的 Go SDK，可将代理嵌入到自己的服务中。

## 支持的提供方

| 提供方 | API 兼容性 | 认证方式 |
| --- | --- | --- |
| OpenAI Codex（GPT 系列） | OpenAI Chat Completions、Responses | OAuth 登录或 `codex-api-key` |
| Anthropic Claude | Anthropic Messages（`/v1/messages`） | OAuth 登录或 `claude-api-key` |
| Google Gemini / Antigravity | Gemini `generateContent`、Gemini Interactions | OAuth 登录或 `gemini-api-key` |
| xAI Grok | OpenAI、Responses | OAuth 登录或 `xai-api-key` |
| Kimi（Moonshot） | OpenAI | OAuth 登录（`--kimi-login`）或 Kimi.ai OAuth 登录（`--kimi-ai-login`） |
| Devin | OpenAI | OAuth 登录 |
| Meta | OpenAI | OAuth 登录或 `meta-api-key` |
| Cursor | OpenAI | API 密钥导入（`--cursor-login` 或管理中心） |
| Vertex AI | Gemini | `vertex-api-key` / 导入服务账号 |
| 任意 OpenAI 兼容上游 | OpenAI | `openai-compatibility` 配置 |

## 快速开始

### 前置要求

- Go 1.26+（从源码构建）或 Docker。
- 至少一个提供方账号（OAuth）和/或 API Key。

### 从源码构建

```bash
git clone https://github.com/UNICKCHENG/CLIProxyAPIPlus.git
cd CLIProxyAPIPlus
go build -o cli-proxy-api ./cmd/server
```

### 准备配置文件

复制示例配置并按需修改：

```bash
cp config.example.yaml config.yaml
```

至少需要设置客户端使用的 API Key 以及管理密钥：

```yaml
port: 8317
auth-dir: "~/.cli-proxy-api"
api-keys:
  - "your-api-key"
remote-management:
  allow-remote: false
  secret-key: "your-management-key"
```

### 启动服务

```bash
./cli-proxy-api --config config.yaml
```

默认监听 `http://localhost:8317`。使用 `--tui` 可启动终端 UI，配合 `--standalone` 可运行内嵌的本地服务。

### 登录提供方账号

运行对应的 OAuth 流程，必要时重启服务：

```bash
./cli-proxy-api --codex-login          # OpenAI Codex
./cli-proxy-api --claude-login         # Anthropic Claude
./cli-proxy-api --antigravity-login    # Google Antigravity / Gemini
./cli-proxy-api --kimi-login           # Kimi (Moonshot, platform.kimi.com)
./cli-proxy-api --kimi-ai-login        # Kimi.ai
./cli-proxy-api --xai-login            # xAI Grok
./cli-proxy-api --devin-login          # Devin
./cli-proxy-api --meta-login           # Meta
./cli-proxy-api --cursor-login         # Cursor（API 密钥导入；非交互式请加 --cursor-api-key）
```

常用可选参数：`--no-browser`（不自动打开浏览器）、`--oauth-callback-port <port>`（覆盖回调端口）、`--config <path>`（指定配置文件路径）。

### 让客户端指向 CLIProxyAPI

将你配置的 API Key 作为客户端凭据，并把本地服务作为 Base URL。

#### OpenAI 兼容客户端

```text
Base URL: http://localhost:8317/v1
API Key:  your-api-key
```

#### Anthropic / Claude Code

```bash
export ANTHROPIC_BASE_URL=http://localhost:8317
export ANTHROPIC_API_KEY=your-api-key
```

#### Gemini 客户端

```text
Base URL: http://localhost:8317
API Key:  your-api-key
```

#### Codex CLI 直连路由

```text
Base URL: http://localhost:8317/backend-api/codex
```

## 配置

配置文件为 `config.yaml`，带注释的完整参考见 [`config.example.yaml`](config.example.yaml)。常用配置项：

| 配置项 | 说明 |
| --- | --- |
| `host` / `port` | 绑定地址与监听端口（默认 `8317`）。 |
| `tls` | 使用证书和私钥启用 HTTPS。 |
| `api-keys` | 客户端访问代理时使用的密钥。 |
| `auth-dir` | 保存提供方凭据的目录。 |
| `remote-management` | 管理 API 开关、密钥与远程访问设置。 |
| `debug` | 启用详细日志。 |
| `proxy-url` | 上游请求使用的出站代理。 |
| `request-retry` / `max-retry-credentials` | 上游调用失败时的重试行为。 |
| `routing` | 轮询与路由策略设置。 |
| `plugins` | 启用与配置插件。 |

工作目录下的 `.env` 会被自动加载。Postgres、Git、对象存储等远程存储后端为可选项，通过 `PGSTORE_*`、`GITSTORE_*`、`OBJECTSTORE_*` 环境变量配置。详见 [`.env.example`](.env.example)。

## 使用 API

| 端点 | 说明 |
| --- | --- |
| `GET /v1/models` | 列出可用模型。 |
| `POST /v1/chat/completions` | OpenAI 兼容的 Chat Completions。 |
| `POST /v1/completions` | OpenAI 兼容的传统 Completions。 |
| `POST /v1/responses` | OpenAI Responses API（支持流式与 WebSocket）。 |
| `POST /v1/messages` | Anthropic Messages API。 |
| `POST /v1/messages/count_tokens` | Anthropic Token 计数。 |
| `POST /v1/images/generations` | 图片生成。 |
| `POST /v1/images/edits` | 图片编辑。 |
| `POST /v1/videos` | 视频生成。 |
| `POST /v1beta/models/*action` | Gemini `generateContent` 及相关操作。 |
| `POST /v1beta/interactions` | Gemini Interactions API。 |
| `POST /v1/realtime` | 实时 / Live 会话。 |
| `GET /healthz` | 健康检查。 |

## 管理 API

管理 API 位于 `/v0/management`，需要管理密钥。启用后可通过 `/management.html` 访问 Web 控制面板。默认禁止远程访问，可通过 `remote-management.allow-remote` 开启。

完整端点列表请参见[管理 API 文档](https://help.router-for.me/cn/management/api)。

## Docker

使用 Docker Compose 构建并运行：

```bash
docker compose up -d
```

或直接构建镜像：

```bash
docker build -t cli-proxy-api .
docker run -p 8317:8317 \
  -v "$(pwd)/config.yaml:/CLIProxyAPI/config.yaml" \
  -v "$(pwd)/auths:/root/.cli-proxy-api" \
  cli-proxy-api
```

`docker-compose.yml` 已将 `config.yaml`、认证目录、日志和插件挂载为卷。

## Go SDK

CLIProxyAPI 可嵌入到 Go 应用中：

- 快速上手：[`docs/sdk-usage_CN.md`](docs/sdk-usage_CN.md)
- 执行器与翻译器：[`docs/sdk-advanced_CN.md`](docs/sdk-advanced_CN.md)
- 访问控制：[`docs/sdk-access_CN.md`](docs/sdk-access_CN.md)
- 凭据监听：[`docs/sdk-watcher_CN.md`](docs/sdk-watcher_CN.md)
- 自定义 Provider 示例：[`examples/custom-provider`](examples/custom-provider)

## 使用量统计

使用量统计不再内置。如需仪表盘与持久化，可使用以下与 CLIProxyAPI 集成的社区项目：

- [CPA Usage Keeper](https://github.com/Willxup/cpa-usage-keeper) —— 独立的使用量持久化与可视化服务，支持定期同步、SQLite 存储、聚合 API 以及内置仪表盘。
- [CPA-Manager-Plus](https://github.com/seakee/CPA-Manager-Plus) —— 完整的管理中心，提供请求级监控、费用预估与 Codex 账号池运维能力。

## 贡献

欢迎贡献！请随时提交 Pull Request。

1. Fork 仓库。
2. 创建功能分支（`git checkout -b feature/amazing-feature`）。
3. 提交更改（`git commit -m 'Add some amazing feature'`）。
4. 推送分支（`git push origin feature/amazing-feature`）。
5. 打开 Pull Request。

## 社区项目

基于 CLIProxyAPI 构建的项目：

- [vibeproxy](https://github.com/automazeio/vibeproxy) —— 原生 macOS 菜单栏应用，在 AI 编程工具中使用 Claude Code 与 ChatGPT 订阅。
- [Subtitle Translator](https://github.com/VjayC/SRT-Subtitle-Translator-Validator) —— 跨平台桌面与 Web 应用，使用已有 LLM 订阅翻译并校验 SRT 字幕。
- [CCS (Claude Code Switch)](https://github.com/kaitranntt/ccs) —— CLI 封装器，可在多个 Claude 账号与替代模型间即时切换。
- [Quotio](https://github.com/nguyenphutrong/quotio) —— 原生 macOS 菜单栏应用，统一 Claude、Gemini、OpenAI 与 Antigravity 订阅并提供配额追踪与自动故障转移。
- [ProxyPilot](https://github.com/Finesssee/ProxyPilot) —— Windows 原生分支，集成 TUI、系统托盘与多提供方 OAuth。
- [Claude Proxy VSCode](https://github.com/uzhao/claude-proxy-vscode) —— VS Code 扩展，用于切换 Claude Code 模型，内置 CLIProxyAPI 作为后端。
- [ZeroLimit](https://github.com/0xtbug/zero-limit) —— Windows 桌面应用，用于监控 AI 编程助手配额。
- [CPA-XXX Panel](https://github.com/ferretgeek/CPA-X) —— 轻量 Web 管理面板，提供健康检查、监控、日志与统计。
- [CLIProxyAPI Tray](https://github.com/kitephp/CLIProxyAPI_Tray) —— 基于 PowerShell 的 Windows 托盘应用。
- [霖君](https://github.com/wangdabaoqq/LinJun) —— 用于管理 AI 编程助手的跨平台桌面应用。
- [CLIProxyAPI Dashboard](https://github.com/itsmylife44/cliproxyapi-dashboard) —— 基于 Next.js、React 与 PostgreSQL 的 Web 管理仪表盘。
- [All API Hub](https://github.com/qixing-jk/all-api-hub) —— 管理 New API 兼容中转账号的浏览器扩展，通过管理 API 集成。
- [Shadow AI](https://github.com/HEUDavid/shadow-ai) —— 面向受限环境设计的 AI 辅助工具。
- [ProxyPal](https://github.com/buddingnewinsights/proxypal) —— 以原生 GUI 封装 CLIProxyAPI 的跨平台桌面应用。
- [CLIProxyAPI Quota Inspector](https://github.com/AllenReder/CLIProxyAPI-Quota-Inspector) —— 跨平台配额查询工具，支持按账号展示配额窗口。
- [CLIProxy Pool Watch](https://github.com/murasame612/CLIProxyPoolWidget) —— 原生 macOS SwiftUI 应用，用于监控 Codex 账号配额。
- [Panopticon](https://github.com/eltmon/panopticon-cli) —— 面向 AI 编程助手的多智能体编排工具。
- [Tunnel Agent](https://github.com/Villoh/tunnel-agent) —— Windows 桌面 UI，用于管理 CLIProxyAPI 与提供方账号。
- [Quotio Desktop](https://github.com/xiaocoss/quotio-desktop) —— Quotio 的跨平台 Tauri 移植版。
- [Universal Chat Provider](https://github.com/maxdewald/vscode-universal-chat-provider) —— 将订阅接入 GitHub Copilot Chat 的 VS Code 扩展。
- [CPA-Tray-Powershell](https://github.com/ztzpro/CPA-Tray-Powershell) —— 带更新校验的 PowerShell Windows 托盘启动器。
- [Grok Search MCP](https://github.com/MapleMapleCat/Grok_Search_Mcp) —— 通过 CLIProxyAPI 提供 Grok 网页搜索的 MCP 服务器。
- [AIUsage](https://github.com/sylearn/AIUsage) —— 原生 macOS SwiftUI 仪表盘与代理管理器。
- [Claude Dialects](https://github.com/stefandevo/claude-dialects) —— 运行多个由不同模型驱动的 Claude Code 命令。
- [WebBrain](https://github.com/webbrain-one/webbrain) —— 可将 CLIProxyAPI 本地端点用作模型提供商的浏览器智能体。
- [Infinitus](https://github.com/deathemperor/infinitus) —— 原生 macOS 菜单栏应用，可运行多个 Claude 账号。
- [PiCloud](https://github.com/cookerpapa/pi-cloud) —— 使用 CLIProxyAPI 作为提供方网关的自托管编程 Agent 平台。
- [cc-status-line](https://github.com/kinka/cc-status-line) —— 展示逐账号配额的 Claude Code 状态栏。

受 CLIProxyAPI 启发的移植版与替代方案：

- [9Router](https://github.com/decolua/9router) —— Next.js 实现，支持格式转换、组合回退与多账号管理。
- [OmniRoute](https://github.com/diegosouzapw/OmniRoute) —— OpenAI 兼容网关，支持智能路由、重试与回退。
- [Codex Switch](https://github.com/9ycrooked/CodexSwitch) —— 使用 Tauri + Vue 管理多个 OpenAI Codex 桌面账号的工具。

> [!NOTE]
> 如果你开发了基于 CLIProxyAPI 的项目或其移植版，请提交 PR 将其添加到此列表。

## 许可证

此项目根据 MIT 许可证授权 —— 详情请参阅 [LICENSE](LICENSE) 文件。
