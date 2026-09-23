# CLIProxyAPI Plus

[English](README.md) | 中文 | [日本語](README_JA.md)

[CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) 的 fork。上游是一个代理服务器：把你已经在用的 CLI 工具和订阅，变成 **OpenAI / Gemini / Claude 兼容的 API**。

本 fork 跟随上游更新，安装、配置、部署都跟原项目一样，完整说明见[上游 README](https://github.com/router-for-me/CLIProxyAPI) 和[官方文档](https://help.router-for.me)。

## 和上游的差异

- **Cursor** —— 把 Cursor API Key 导入成账号（`--cursor-login`，或在管理中心里操作），之后就能像其他 provider 一样路由请求。
- **用量与费用统计** —— 按模型、provider、账号统计。打开 `usage-statistics-enabled: true`，在管理中心或 `/v0/management/usage-stats` 查看。
- **更干净的管理面板** —— 内置 Web 面板是本 fork 的 [Cli-Proxy-API-Plus-Management-Center](https://github.com/UNICKCHENG/Cli-Proxy-API-Plus-Management-Center)，去掉了推广内容。


## 快速开始

### 安装包

到 [Releases](https://github.com/UNICKCHENG/CLIProxyAPIPlus/releases) 下载对应平台（macOS / Linux / Windows）的压缩包，然后：

```bash
cp config.example.yaml config.yaml
# 编辑 config.yaml，填入 api-keys
./cli-proxy-api --config config.yaml
```

### Docker

用的是本仓库的 `docker-compose.yml`，配置、授权目录、日志的挂载都已经写好。

```bash
docker compose up -d
```

拉取镜像失败时，先执行 `docker login ghcr.io`。

### 从源码编译

```bash
git clone https://github.com/UNICKCHENG/CLIProxyAPIPlus.git
cd CLIProxyAPIPlus
cp config.example.yaml config.yaml
# 编辑 config.yaml，填入 api-keys
go build -o cli-proxy-api ./cmd/server
./cli-proxy-api --config config.yaml
```

### 登录 provider 账号

```bash
./cli-proxy-api --codex-login
./cli-proxy-api --claude-login
./cli-proxy-api --cursor-login
```

全部可用的登录方式见 `./cli-proxy-api --help`。

## 文档

- 上游 README：https://github.com/router-for-me/CLIProxyAPI
- 官方文档：https://help.router-for.me

## 许可证

MIT —— 见 [LICENSE](LICENSE)。
