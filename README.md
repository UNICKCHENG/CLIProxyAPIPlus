# CLIProxyAPI Plus

English | [中文](README_CN.md) | [日本語](README_JA.md)

A fork of [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) — a proxy that turns the CLI tools and subscriptions you already pay for into **OpenAI-, Gemini-, and Claude-compatible APIs**.

This fork keeps up with upstream, so installing, configuring, and deploying work exactly like the original project. The [upstream README](https://github.com/router-for-me/CLIProxyAPI) and [official docs](https://help.router-for.me) are the full guide.

## What's different here

- **Cursor** — import a Cursor API key as an account (`--cursor-login`, or from the Management Center) and route requests to it like any other provider.
- **Usage and cost statistics** — per model, provider, and account. Turn on `usage-statistics-enabled: true`, then view them in the Management Center or via `/v0/management/usage-stats`.
- **A cleaner management panel** — the bundled web panel is [Cli-Proxy-API-Plus-Management-Center](https://github.com/UNICKCHENG/Cli-Proxy-API-Plus-Management-Center), with the promotional content removed.

## Quick start

### Release package

Download the archive for your platform (macOS / Linux / Windows) from [Releases](https://github.com/UNICKCHENG/CLIProxyAPIPlus/releases), then:

```bash
cp config.example.yaml config.yaml
# edit config.yaml, fill in api-keys
./cli-proxy-api --config config.yaml
```

### Docker

Uses the `docker-compose.yml` in this repo. The config, auth directory, and logs are already mounted.

```bash
docker compose up -d
```

### Build from source

```bash
git clone https://github.com/UNICKCHENG/CLIProxyAPIPlus.git
cd CLIProxyAPIPlus
cp config.example.yaml config.yaml
# edit config.yaml, fill in api-keys
go build -o cli-proxy-api ./cmd/server
./cli-proxy-api --config config.yaml
```

### Sign in to providers

```bash
./cli-proxy-api --codex-login
./cli-proxy-api --claude-login
./cli-proxy-api --cursor-login
```

Run `./cli-proxy-api --help` for the full list.

## Documentation

- Upstream README: https://github.com/router-for-me/CLIProxyAPI
- Official docs: https://help.router-for.me

## License

MIT — see [LICENSE](LICENSE).
