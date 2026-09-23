# CLIProxyAPI Plus

[English](README.md) | [中文](README_CN.md) | 日本語

[CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) のフォークです。元のプロジェクトは、すでに使っている CLI ツールとサブスクリプションを **OpenAI / Gemini / Claude 互換の API** にするプロキシサーバーです。

このフォークはアップストリームに追従しています。インストール・設定・デプロイの方法は元のプロジェクトと同じで、詳しくは[アップストリームの README](https://github.com/router-for-me/CLIProxyAPI) と[公式ドキュメント](https://help.router-for.me)を参照してください。

## 上流との違い

- **Cursor** — Cursor の API キーをアカウントとしてインポートし（`--cursor-login`、または管理センターから）、他のプロバイダーと同じようにリクエストを振り分けられます。
- **使用量とコストの統計** — モデル・プロバイダー・アカウントごとに集計。`usage-statistics-enabled: true` を有効にすると、管理センターまたは `/v0/management/usage-stats` で確認できます。
- **すっきりした管理パネル** — 同梱の Web パネルは本フォークの [Cli-Proxy-API-Plus-Management-Center](https://github.com/UNICKCHENG/Cli-Proxy-API-Plus-Management-Center) で、宣伝コンテンツを削除しています。

## クイックスタート

### リリース版のパッケージ

[Releases](https://github.com/UNICKCHENG/CLIProxyAPIPlus/releases) からお使いのプラットフォーム（macOS / Linux / Windows）のアーカイブをダウンロードして展開します。

```bash
cp config.example.yaml config.yaml
# config.yaml を編集して api-keys を設定
./cli-proxy-api --config config.yaml
```

### Docker

このリポジトリの `docker-compose.yml` を使います。設定ファイル・認証ディレクトリ・ログのマウントは設定済みです。

```bash
docker compose up -d
```

イメージの取得に失敗する場合は、先に `docker login ghcr.io` を実行してください。

### ソースからビルド

```bash
git clone https://github.com/UNICKCHENG/CLIProxyAPIPlus.git
cd CLIProxyAPIPlus
cp config.example.yaml config.yaml
# config.yaml を編集して api-keys を設定
go build -o cli-proxy-api ./cmd/server
./cli-proxy-api --config config.yaml
```

### プロバイダーへのログイン

```bash
./cli-proxy-api --codex-login
./cli-proxy-api --claude-login
./cli-proxy-api --cursor-login
```

利用できるログイン方法の一覧は `./cli-proxy-api --help` で確認できます。

## ドキュメント

- アップストリームの README：https://github.com/router-for-me/CLIProxyAPI
- 公式ドキュメント：https://help.router-for.me

## ライセンス

MIT —— [LICENSE](LICENSE) を参照してください。
