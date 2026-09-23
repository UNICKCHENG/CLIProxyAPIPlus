# CLIProxyAPI

English | [中文](README_CN.md) | [日本語](README_JA.md)

CLIProxyAPI is a proxy server that exposes **OpenAI-, Gemini-, Claude-, Codex-, and Grok-compatible APIs** on top of the CLI tools and subscriptions you already use. Run it locally (or on a server), sign in to your provider accounts once, and point any compatible client or SDK at a single endpoint. CLIProxyAPI handles protocol translation, account rotation, load balancing, streaming, tool calling, and WebSocket where the provider supports it.

It is useful when you want to:

- Use Claude Code, Codex CLI, Gemini CLI, and other CLI tools through any OpenAI-, Anthropic-, or Gemini-compatible client or SDK.
- Pool several accounts per provider and spread requests with round-robin load balancing.
- Reuse existing CLI subscriptions instead of manually managing raw API keys.
- Embed a multi-provider gateway into your own Go application via the reusable SDK.

## Table of Contents

- [Features](#features)
- [Supported Providers](#supported-providers)
- [Quick Start](#quick-start)
- [Configuration](#configuration)
- [Using the API](#using-the-api)
- [Management API](#management-api)
- [Docker](#docker)
- [Go SDK](#go-sdk)
- [Usage Statistics](#usage-statistics)
- [Contributing](#contributing)
- [Community Projects](#community-projects)
- [License](#license)

## Features

- OpenAI-, Gemini-, Claude-, Codex-, and Grok-compatible API endpoints for CLI models.
- OAuth login for supported providers (Codex, Claude Code, Antigravity/Gemini, Grok, Kimi, Devin, Meta).
- API-key support for Gemini AI Studio, Codex, Claude, xAI, Meta, and Vertex.
- Streaming, non-streaming, and WebSocket responses where supported.
- Function calling / tool use and multimodal input (text and images).
- Multiple accounts per provider with round-robin load balancing and automatic failover.
- Simple CLI authentication flows for every OAuth provider.
- OpenAI-compatible upstream providers configured in YAML (for example OpenRouter).
- Request retries, credential cooldowns, and per-provider overrides.
- Management API and optional web control panel.
- mDNS / DNS-SD discovery for local AI gateways.
- Plugins for extending provider behavior.
- A reusable Go SDK for embedding the proxy in your own service.

## Supported Providers

| Provider | API compatibility | Authentication |
| --- | --- | --- |
| OpenAI Codex (GPT series) | OpenAI Chat Completions, Responses | OAuth login or `codex-api-key` |
| Anthropic Claude | Anthropic Messages (`/v1/messages`) | OAuth login or `claude-api-key` |
| Google Gemini / Antigravity | Gemini `generateContent`, Gemini Interactions | OAuth login or `gemini-api-key` |
| xAI Grok | OpenAI, Responses | OAuth login or `xai-api-key` |
| Kimi (Moonshot) | OpenAI | OAuth login (`--kimi-login`) or Kimi.ai OAuth login (`--kimi-ai-login`) |
| Devin | OpenAI | OAuth login |
| Meta | OpenAI | OAuth login or `meta-api-key` |
| Cursor | OpenAI | API-key import (`--cursor-login` or Management Center) |
| Vertex AI | Gemini | `vertex-api-key` / service-account import |
| Any OpenAI-compatible upstream | OpenAI | `openai-compatibility` config |

## Quick Start

### Prerequisites

- Go 1.26+ (to build from source), or Docker.
- At least one provider account (OAuth) and/or API key.

### Build from source

```bash
git clone https://github.com/UNICKCHENG/CLIProxyAPIPlus.git
cd CLIProxyAPIPlus
go build -o cli-proxy-api ./cmd/server
```

### Prepare a config file

Copy the example configuration and edit it:

```bash
cp config.example.yaml config.yaml
```

At minimum, set an API key that clients will use and a management key:

```yaml
port: 8317
auth-dir: "~/.cli-proxy-api"
api-keys:
  - "your-api-key"
remote-management:
  allow-remote: false
  secret-key: "your-management-key"
```

### Start the server

```bash
./cli-proxy-api --config config.yaml
```

The server listens on `http://localhost:8317` by default. It also starts an optional terminal UI with `--tui`, and can run an embedded local server with `--standalone`.

### Sign in to providers

Run the matching OAuth flow, then restart the server if needed:

```bash
./cli-proxy-api --codex-login          # OpenAI Codex
./cli-proxy-api --claude-login         # Anthropic Claude
./cli-proxy-api --antigravity-login    # Google Antigravity / Gemini
./cli-proxy-api --kimi-login           # Kimi (Moonshot, platform.kimi.com)
./cli-proxy-api --kimi-ai-login        # Kimi.ai
./cli-proxy-api --xai-login            # xAI Grok
./cli-proxy-api --devin-login          # Devin
./cli-proxy-api --meta-login           # Meta
./cli-proxy-api --cursor-login         # Cursor (API-key import; --cursor-api-key for non-interactive)
```

Useful optional flags: `--no-browser` (do not open a browser automatically), `--oauth-callback-port <port>` (override the callback port), and `--config <path>` (config file location).

### Point your client at CLIProxyAPI

Use your configured API key as the client credential and the local server as the base URL.

#### OpenAI-compatible clients

```text
Base URL: http://localhost:8317/v1
API key:  your-api-key
```

#### Anthropic / Claude Code

```bash
export ANTHROPIC_BASE_URL=http://localhost:8317
export ANTHROPIC_API_KEY=your-api-key
```

#### Gemini clients

```text
Base URL: http://localhost:8317
API key:  your-api-key
```

#### Codex CLI direct route

```text
Base URL: http://localhost:8317/backend-api/codex
```

## Configuration

Configuration lives in `config.yaml`. The full, commented reference is [`config.example.yaml`](config.example.yaml). Commonly used options:

| Key | Description |
| --- | --- |
| `host` / `port` | Bind address and listening port (default `8317`). |
| `tls` | Enable HTTPS with a certificate and key. |
| `api-keys` | Keys clients use to authenticate against the proxy. |
| `auth-dir` | Directory where provider credentials are stored. |
| `remote-management` | Management API switch, key, and remote access. |
| `debug` | Enable verbose logging. |
| `proxy-url` | Outbound proxy for upstream requests. |
| `request-retry` / `max-retry-credentials` | Retry behavior for failed upstream calls. |
| `routing` | Round-robin and routing strategy settings. |
| `plugins` | Enable and configure plugins. |

Environment variables are auto-loaded from `.env` in the working directory. Remote storage backends (Postgres, Git, object storage) are optional and configured through `PGSTORE_*`, `GITSTORE_*`, and `OBJECTSTORE_*` variables. See [`.env.example`](.env.example).

## Using the API

| Endpoint | Description |
| --- | --- |
| `GET /v1/models` | List available models. |
| `POST /v1/chat/completions` | OpenAI-compatible chat completions. |
| `POST /v1/completions` | OpenAI-compatible legacy completions. |
| `POST /v1/responses` | OpenAI Responses API (streaming and WebSocket). |
| `POST /v1/messages` | Anthropic Messages API. |
| `POST /v1/messages/count_tokens` | Anthropic token counting. |
| `POST /v1/images/generations` | Image generation. |
| `POST /v1/images/edits` | Image edits. |
| `POST /v1/videos` | Video generation. |
| `POST /v1beta/models/*action` | Gemini `generateContent` and related actions. |
| `POST /v1beta/interactions` | Gemini Interactions API. |
| `POST /v1/realtime` | Realtime / live sessions. |
| `GET /healthz` | Health check. |

## Management API

The Management API is served under `/v0/management` and requires the management key. When enabled, a web control panel is available at `/management.html`. Remote access is disabled by default and can be enabled with `remote-management.allow-remote`.

See the [Management API documentation](https://help.router-for.me/management/api) for the full endpoint list.

## Docker

Build and run with Docker Compose:

```bash
docker compose up -d
```

Or build the image directly:

```bash
docker build -t cli-proxy-api .
docker run -p 8317:8317 \
  -v "$(pwd)/config.yaml:/CLIProxyAPI/config.yaml" \
  -v "$(pwd)/auths:/root/.cli-proxy-api" \
  cli-proxy-api
```

`docker-compose.yml` mounts `config.yaml`, the auth directory, logs, and plugins as volumes.

## Go SDK

CLIProxyAPI can be embedded in Go applications:

- Getting started: [`docs/sdk-usage.md`](docs/sdk-usage.md)
- Executors and translators: [`docs/sdk-advanced.md`](docs/sdk-advanced.md)
- Access control: [`docs/sdk-access.md`](docs/sdk-access.md)
- Credential watcher: [`docs/sdk-watcher.md`](docs/sdk-watcher.md)
- Custom provider example: [`examples/custom-provider`](examples/custom-provider)

## Usage Statistics

Usage statistics are not built in. If you need dashboards and persistence, these community projects integrate with CLIProxyAPI:

- [CPA Usage Keeper](https://github.com/Willxup/cpa-usage-keeper) — standalone persistence and visualization service with periodic sync, SQLite storage, aggregate APIs, and a built-in dashboard.
- [CPA-Manager-Plus](https://github.com/seakee/CPA-Manager-Plus) — full management center with request-level monitoring, cost estimates, and Codex account-pool operations.

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

1. Fork the repository.
2. Create your feature branch (`git checkout -b feature/amazing-feature`).
3. Commit your changes (`git commit -m 'Add some amazing feature'`).
4. Push to the branch (`git push origin feature/amazing-feature`).
5. Open a Pull Request.

## Community Projects

Projects built on CLIProxyAPI:

- [vibeproxy](https://github.com/automazeio/vibeproxy) — native macOS menu bar app to use your Claude Code & ChatGPT subscriptions with AI coding tools.
- [Subtitle Translator](https://github.com/VjayC/SRT-Subtitle-Translator-Validator) — cross-platform desktop and web app to translate and validate SRT subtitles using your existing LLM subscriptions.
- [CCS (Claude Code Switch)](https://github.com/kaitranntt/ccs) — CLI wrapper for instant switching between multiple Claude accounts and alternative models.
- [Quotio](https://github.com/nguyenphutrong/quotio) — native macOS menu bar app that unifies Claude, Gemini, OpenAI, and Antigravity subscriptions with quota tracking and auto-failover.
- [ProxyPilot](https://github.com/Finesssee/ProxyPilot) — Windows-native fork with TUI, system tray, and multi-provider OAuth.
- [Claude Proxy VSCode](https://github.com/uzhao/claude-proxy-vscode) — VS Code extension for switching Claude Code models, backed by an embedded CLIProxyAPI.
- [ZeroLimit](https://github.com/0xtbug/zero-limit) — Windows desktop app for monitoring AI coding assistant quotas.
- [CPA-XXX Panel](https://github.com/ferretgeek/CPA-X) — lightweight web admin panel with health checks, monitoring, logs, and statistics.
- [CLIProxyAPI Tray](https://github.com/kitephp/CLIProxyAPI_Tray) — PowerShell-based Windows tray application.
- [霖君](https://github.com/wangdabaoqq/LinJun) — cross-platform desktop app for managing AI coding assistants.
- [CLIProxyAPI Dashboard](https://github.com/itsmylife44/cliproxyapi-dashboard) — web management dashboard built with Next.js, React, and PostgreSQL.
- [All API Hub](https://github.com/qixing-jk/all-api-hub) — browser extension for managing New API-compatible relay accounts, integrated through the Management API.
- [Shadow AI](https://github.com/HEUDavid/shadow-ai) — AI assistant tool designed for restricted environments.
- [ProxyPal](https://github.com/buddingnewinsights/proxypal) — cross-platform desktop GUI wrapping CLIProxyAPI.
- [CLIProxyAPI Quota Inspector](https://github.com/AllenReder/CLIProxyAPI-Quota-Inspector) — cross-platform quota inspector with per-account quota windows.
- [CLIProxy Pool Watch](https://github.com/murasame612/CLIProxyPoolWidget) — native macOS SwiftUI app for monitoring Codex account quotas.
- [Panopticon](https://github.com/eltmon/panopticon-cli) — multi-agent orchestration for AI coding assistants.
- [Tunnel Agent](https://github.com/Villoh/tunnel-agent) — Windows desktop UI for managing CLIProxyAPI and provider accounts.
- [Quotio Desktop](https://github.com/xiaocoss/quotio-desktop) — cross-platform Tauri port of Quotio.
- [Universal Chat Provider](https://github.com/maxdewald/vscode-universal-chat-provider) — VS Code extension that brings subscriptions into GitHub Copilot Chat.
- [CPA-Tray-Powershell](https://github.com/ztzpro/CPA-Tray-Powershell) — PowerShell Windows tray launcher with update verification.
- [Grok Search MCP](https://github.com/MapleMapleCat/Grok_Search_Mcp) — MCP server providing Grok-powered web search through CLIProxyAPI.
- [AIUsage](https://github.com/sylearn/AIUsage) — native macOS SwiftUI dashboard and proxy manager.
- [Claude Dialects](https://github.com/stefandevo/claude-dialects) — run multiple Claude Code commands, each backed by a different model.
- [WebBrain](https://github.com/webbrain-one/webbrain) — browser agent that can use CLIProxyAPI's local endpoint as a model provider.
- [Infinitus](https://github.com/deathemperor/infinitus) — native macOS menu bar app for running a fleet of Claude accounts.
- [PiCloud](https://github.com/cookerpapa/pi-cloud) — self-hosted coding-agent platform using CLIProxyAPI as its provider gateway.
- [cc-status-line](https://github.com/kinka/cc-status-line) — Claude Code status line showing per-account quotas.
- [CLIProxy Quota Tray](https://github.com/ZYHUO/CLIProxy-Quota-Tray) — cross-platform Electron tray dashboard showing per-account OAuth quota windows across ChatGPT/Codex, Claude, Gemini/Antigravity, Grok, Kimi, and Cursor, with usage-queue cost estimates and OpenAI/Claude service status tracking.

Community ports and alternatives inspired by CLIProxyAPI:

- [9Router](https://github.com/decolua/9router) — Next.js implementation with format translation, combo fallback, and multi-account management.
- [OmniRoute](https://github.com/diegosouzapw/OmniRoute) — OpenAI-compatible gateway with smart routing, retries, and fallbacks.
- [Codex Switch](https://github.com/9ycrooked/CodexSwitch) — Tauri + Vue tool for managing multiple OpenAI Codex desktop accounts.

> [!NOTE]
> If you built a project on top of CLIProxyAPI, or a port inspired by it, please open a PR to add it to this list.

## License

This project is licensed under the MIT License — see the [LICENSE](LICENSE) file for details.
