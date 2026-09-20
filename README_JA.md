# CLIProxyAPI

[English](README.md) | [中文](README_CN.md) | 日本語

CLIProxyAPI は、既存の CLI ツールとサブスクリプションの上に **OpenAI / Gemini / Claude / Codex / Grok 互換の API** を公開するプロキシサーバーです。ローカル（またはサーバー）で実行し、プロバイダーアカウントに一度サインインするだけで、互換性のある任意のクライアントや SDK を単一のエンドポイントに向けられます。プロトコル変換、アカウントのローテーション、負荷分散、ストリーミング、ツール呼び出し、およびプロバイダーが対応する場合の WebSocket を CLIProxyAPI が処理します。

次のような場合に便利です。

- 任意の OpenAI / Anthropic / Gemini 互換クライアントや SDK から、Claude Code、Codex CLI、Gemini CLI などの CLI ツールを利用する。
- プロバイダーごとに複数アカウントをまとめ、ラウンドロビンでリクエストを分散する。
- 生の API キーを手動管理せず、既存の CLI サブスクリプションを再利用する。
- 再利用可能な SDK を通じて、マルチプロバイダーゲートウェイを自作の Go アプリケーションに組み込む。

## 目次

- [機能](#機能)
- [対応プロバイダー](#対応プロバイダー)
- [クイックスタート](#クイックスタート)
- [設定](#設定)
- [API の利用](#api-の利用)
- [管理 API](#管理-api)
- [Docker](#docker)
- [Go SDK](#go-sdk)
- [使用量統計](#使用量統計)
- [コントリビューション](#コントリビューション)
- [コミュニティプロジェクト](#コミュニティプロジェクト)
- [ライセンス](#ライセンス)

## 機能

- CLI モデル向けの OpenAI / Gemini / Claude / Codex / Grok 互換 API エンドポイント。
- 対応プロバイダーの OAuth ログイン（Codex、Claude Code、Antigravity / Gemini、Grok、Kimi、Devin、Meta）。
- Gemini AI Studio、Codex、Claude、xAI、Meta、Vertex の API キー対応。
- 対応環境でのストリーミング、非ストリーミング、WebSocket レスポンス。
- 関数呼び出し / ツール利用とマルチモーダル入力（テキストと画像）。
- プロバイダーごとの複数アカウントによるラウンドロビン負荷分散と自動フェイルオーバー。
- 各 OAuth プロバイダー向けのシンプルな CLI 認証フロー。
- YAML 設定による OpenAI 互換アップストリームプロバイダー（例：OpenRouter）。
- リクエスト再試行、クレデンシャルのクールダウン、プロバイダー単位の上書き設定。
- 管理 API とオプションの Web コントロールパネル。
- ローカルネットワーク向けの mDNS / DNS-SD サービス検出。
- プロバイダーの挙動を拡張するプラグイン。
- サービスに組み込める再利用可能な Go SDK。

## 対応プロバイダー

| プロバイダー | API 互換性 | 認証 |
| --- | --- | --- |
| OpenAI Codex（GPT シリーズ） | OpenAI Chat Completions、Responses | OAuth ログインまたは `codex-api-key` |
| Anthropic Claude | Anthropic Messages（`/v1/messages`） | OAuth ログインまたは `claude-api-key` |
| Google Gemini / Antigravity | Gemini `generateContent`、Gemini Interactions | OAuth ログインまたは `gemini-api-key` |
| xAI Grok | OpenAI、Responses | OAuth ログインまたは `xai-api-key` |
| Kimi（Moonshot） | OpenAI | OAuth ログイン |
| Devin | OpenAI | OAuth ログイン |
| Meta | OpenAI | OAuth ログインまたは `meta-api-key` |
| Vertex AI | Gemini | `vertex-api-key` / サービスアカウントのインポート |
| OpenAI 互換アップストリーム | OpenAI | `openai-compatibility` 設定 |

## クイックスタート

### 前提条件

- Go 1.26+（ソースからビルドする場合）または Docker。
- 少なくとも 1 つのプロバイダーアカウント（OAuth）および / または API キー。

### ソースからビルド

```bash
git clone https://github.com/UNICKCHENG/CLIProxyAPIPlus.git
cd CLIProxyAPIPlus
go build -o cli-proxy-api ./cmd/server
```

### 設定ファイルの準備

サンプル設定をコピーして編集します。

```bash
cp config.example.yaml config.yaml
```

最低限、クライアントが使用する API キーと管理キーを設定します。

```yaml
port: 8317
auth-dir: "~/.cli-proxy-api"
api-keys:
  - "your-api-key"
remote-management:
  allow-remote: false
  secret-key: "your-management-key"
```

### サーバーの起動

```bash
./cli-proxy-api --config config.yaml
```

デフォルトでは `http://localhost:8317` で待ち受けます。`--tui` でターミナル UI を起動でき、`--standalone` と組み合わせると組み込みのローカルサーバーを実行します。

### プロバイダーへのサインイン

対応する OAuth フローを実行し、必要に応じてサーバーを再起動します。

```bash
./cli-proxy-api --codex-login          # OpenAI Codex
./cli-proxy-api --claude-login         # Anthropic Claude
./cli-proxy-api --antigravity-login    # Google Antigravity / Gemini
./cli-proxy-api --kimi-login           # Kimi
./cli-proxy-api --xai-login            # xAI Grok
./cli-proxy-api --devin-login          # Devin
./cli-proxy-api --meta-login           # Meta
```

主なオプション：`--no-browser`（ブラウザを自動で開かない）、`--oauth-callback-port <port>`（コールバックポートの上書き）、`--config <path>`（設定ファイルのパス指定）。

### クライアントを CLIProxyAPI に向ける

設定した API キーをクライアントの認証情報として使用し、ローカルサーバーをベース URL に指定します。

#### OpenAI 互換クライアント

```text
Base URL: http://localhost:8317/v1
API key:  your-api-key
```

#### Anthropic / Claude Code

```bash
export ANTHROPIC_BASE_URL=http://localhost:8317
export ANTHROPIC_API_KEY=your-api-key
```

#### Gemini クライアント

```text
Base URL: http://localhost:8317
API key:  your-api-key
```

#### Codex CLI 直接ルート

```text
Base URL: http://localhost:8317/backend-api/codex
```

## 設定

設定は `config.yaml` で行います。コメント付きの完全なリファレンスは [`config.example.yaml`](config.example.yaml) を参照してください。よく使う項目：

| キー | 説明 |
| --- | --- |
| `host` / `port` | バインドアドレスと待ち受けポート（デフォルト `8317`）。 |
| `tls` | 証明書と鍵による HTTPS の有効化。 |
| `api-keys` | クライアントがプロキシの認証に使用するキー。 |
| `auth-dir` | プロバイダーの認証情報を保存するディレクトリ。 |
| `remote-management` | 管理 API のスイッチ、キー、リモートアクセス設定。 |
| `debug` | 詳細ログの有効化。 |
| `proxy-url` | アップストリームリクエスト用の送信プロキシ。 |
| `request-retry` / `max-retry-credentials` | アップストリーム呼び出し失敗時の再試行動作。 |
| `routing` | ラウンドロビンとルーティング戦略の設定。 |
| `plugins` | プラグインの有効化と設定。 |

作業ディレクトリの `.env` は自動的に読み込まれます。Postgres、Git、オブジェクトストレージなどのリモートストレージバックエンドは任意で、`PGSTORE_*`、`GITSTORE_*`、`OBJECTSTORE_*` 環境変数で設定します。詳細は [`.env.example`](.env.example) を参照してください。

## API の利用

| エンドポイント | 説明 |
| --- | --- |
| `GET /v1/models` | 利用可能なモデルの一覧。 |
| `POST /v1/chat/completions` | OpenAI 互換の Chat Completions。 |
| `POST /v1/completions` | OpenAI 互換のレガシー Completions。 |
| `POST /v1/responses` | OpenAI Responses API（ストリーミングと WebSocket）。 |
| `POST /v1/messages` | Anthropic Messages API。 |
| `POST /v1/messages/count_tokens` | Anthropic のトークンカウント。 |
| `POST /v1/images/generations` | 画像生成。 |
| `POST /v1/images/edits` | 画像編集。 |
| `POST /v1/videos` | 動画生成。 |
| `POST /v1beta/models/*action` | Gemini `generateContent` および関連アクション。 |
| `POST /v1beta/interactions` | Gemini Interactions API。 |
| `POST /v1/realtime` | リアルタイム / ライブセッション。 |
| `GET /healthz` | ヘルスチェック。 |

## 管理 API

管理 API は `/v0/management` 以下で提供され、管理キーが必要です。有効にすると `/management.html` で Web コントロールパネルを利用できます。リモートアクセスはデフォルトで無効で、`remote-management.allow-remote` で有効化できます。

完全なエンドポイント一覧は[管理 API ドキュメント](https://help.router-for.me/management/api)を参照してください。

## Docker

Docker Compose でビルドして実行します。

```bash
docker compose up -d
```

またはイメージを直接ビルドします。

```bash
docker build -t cli-proxy-api .
docker run -p 8317:8317 \
  -v "$(pwd)/config.yaml:/CLIProxyAPI/config.yaml" \
  -v "$(pwd)/auths:/root/.cli-proxy-api" \
  cli-proxy-api
```

`docker-compose.yml` は `config.yaml`、認証ディレクトリ、ログ、プラグインをボリュームとしてマウントします。

## Go SDK

CLIProxyAPI は Go アプリケーションに組み込めます。

- はじめに：[`docs/sdk-usage.md`](docs/sdk-usage.md)
- エグゼキューターとトランスレーター：[`docs/sdk-advanced.md`](docs/sdk-advanced.md)
- アクセス制御：[`docs/sdk-access.md`](docs/sdk-access.md)
- クレデンシャルウォッチャー：[`docs/sdk-watcher.md`](docs/sdk-watcher.md)
- カスタムプロバイダーの例：[`examples/custom-provider`](examples/custom-provider)

## 使用量統計

使用量統計は内蔵されていません。ダッシュボードや永続化が必要な場合は、CLIProxyAPI と連携する以下のコミュニティプロジェクトを利用してください。

- [CPA Usage Keeper](https://github.com/Willxup/cpa-usage-keeper) — 定期同期、SQLite ストレージ、集約 API、内蔵ダッシュボードを備えた独立系の使用量永続化・可視化サービス。
- [CPA-Manager-Plus](https://github.com/seakee/CPA-Manager-Plus) — リクエスト単位の監視、コスト見積もり、Codex アカウントプール運用を備えた管理センター。

## コントリビューション

コントリビューションを歓迎します。お気軽に Pull Request を送ってください。

1. リポジトリをフォークする。
2. 機能ブランチを作成する（`git checkout -b feature/amazing-feature`）。
3. 変更をコミットする（`git commit -m 'Add some amazing feature'`）。
4. ブランチにプッシュする（`git push origin feature/amazing-feature`）。
5. Pull Request を開く。

## コミュニティプロジェクト

CLIProxyAPI を基盤とするプロジェクト：

- [vibeproxy](https://github.com/automazeio/vibeproxy) — Claude Code と ChatGPT のサブスクリプションを AI コーディングツールで利用するネイティブ macOS メニューバーアプリ。
- [Subtitle Translator](https://github.com/VjayC/SRT-Subtitle-Translator-Validator) — 既存の LLM サブスクリプションで SRT 字幕を翻訳・検証するクロスプラットフォームアプリ。
- [CCS (Claude Code Switch)](https://github.com/kaitranntt/ccs) — 複数の Claude アカウントと代替モデルを即時切り替える CLI ラッパー。
- [Quotio](https://github.com/nguyenphutrong/quotio) — Claude、Gemini、OpenAI、Antigravity のサブスクリプションを統合し、クォータ追跡と自動フェイルオーバーを提供するネイティブ macOS メニューバーアプリ。
- [ProxyPilot](https://github.com/Finesssee/ProxyPilot) — TUI、システムトレイ、マルチプロバイダー OAuth を備えた Windows ネイティブフォーク。
- [Claude Proxy VSCode](https://github.com/uzhao/claude-proxy-vscode) — 組み込みの CLIProxyAPI をバックエンドとする Claude Code モデル切り替え用 VS Code 拡張。
- [ZeroLimit](https://github.com/0xtbug/zero-limit) — AI コーディングアシスタントのクォータを監視する Windows デスクトップアプリ。
- [CPA-XXX Panel](https://github.com/ferretgeek/CPA-X) — ヘルスチェック、監視、ログ、統計を備えた軽量 Web 管理パネル。
- [CLIProxyAPI Tray](https://github.com/kitephp/CLIProxyAPI_Tray) — PowerShell ベースの Windows トレイアプリ。
- [霖君](https://github.com/wangdabaoqq/LinJun) — AI コーディングアシスタントを管理するクロスプラットフォームデスクトップアプリ。
- [CLIProxyAPI Dashboard](https://github.com/itsmylife44/cliproxyapi-dashboard) — Next.js、React、PostgreSQL で構築された Web 管理ダッシュボード。
- [All API Hub](https://github.com/qixing-jk/all-api-hub) — 管理 API 経由で連携する New API 互換リレーアカウント管理のブラウザ拡張。
- [Shadow AI](https://github.com/HEUDavid/shadow-ai) — 制限環境向けに設計された AI アシスタントツール。
- [ProxyPal](https://github.com/buddingnewinsights/proxypal) — CLIProxyAPI をネイティブ GUI でラップするクロスプラットフォームデスクトップアプリ。
- [CLIProxyAPI Quota Inspector](https://github.com/AllenReder/CLIProxyAPI-Quota-Inspector) — アカウント別のクォータウィンドウを表示するクロスプラットフォームのクォータ確認ツール。
- [CLIProxy Pool Watch](https://github.com/murasame612/CLIProxyPoolWidget) — Codex アカウントのクォータを監視するネイティブ macOS SwiftUI アプリ。
- [Panopticon](https://github.com/eltmon/panopticon-cli) — AI コーディングアシスタント向けのマルチエージェントオーケストレーション。
- [Tunnel Agent](https://github.com/Villoh/tunnel-agent) — CLIProxyAPI とプロバイダーアカウントを管理する Windows デスクトップ UI。
- [Quotio Desktop](https://github.com/xiaocoss/quotio-desktop) — Quotio のクロスプラットフォーム（Tauri）移植版。
- [Universal Chat Provider](https://github.com/maxdewald/vscode-universal-chat-provider) — サブスクリプションを GitHub Copilot Chat に接続する VS Code 拡張。
- [CPA-Tray-Powershell](https://github.com/ztzpro/CPA-Tray-Powershell) — 更新検証機能付きの PowerShell Windows トレイランチャー。
- [Grok Search MCP](https://github.com/MapleMapleCat/Grok_Search_Mcp) — CLIProxyAPI 経由で Grok の Web 検索を提供する MCP サーバー。
- [AIUsage](https://github.com/sylearn/AIUsage) — ネイティブ macOS SwiftUI ダッシュボード兼プロキシマネージャー。
- [Claude Dialects](https://github.com/stefandevo/claude-dialects) — それぞれ異なるモデルで動く複数の Claude Code コマンドを実行。
- [WebBrain](https://github.com/webbrain-one/webbrain) — CLIProxyAPI のローカルエンドポイントをモデルプロバイダーとして利用できるブラウザエージェント。
- [Infinitus](https://github.com/deathemperor/infinitus) — 複数の Claude アカウントを運用するネイティブ macOS メニューバーアプリ。
- [PiCloud](https://github.com/cookerpapa/pi-cloud) — CLIProxyAPI をプロバイダーゲートウェイとして使うセルフホスト型コーディングエージェント基盤。
- [cc-status-line](https://github.com/kinka/cc-status-line) — アカウント別クォータを表示する Claude Code ステータスライン。

CLIProxyAPI に触発された移植版・代替プロジェクト：

- [9Router](https://github.com/decolua/9router) — フォーマット変換、コンボフォールバック、マルチアカウント管理を備えた Next.js 実装。
- [OmniRoute](https://github.com/diegosouzapw/OmniRoute) — スマートルーティング、リトライ、フォールバックを備えた OpenAI 互換ゲートウェイ。
- [Codex Switch](https://github.com/9ycrooked/CodexSwitch) — 複数の OpenAI Codex デスクトップアカウントを管理する Tauri + Vue ツール。

> [!NOTE]
> CLIProxyAPI を基盤としたプロジェクトや、それに触発された移植版を開発した場合は、PR を送ってこのリストに追加してください。

## ライセンス

本プロジェクトは MIT ライセンスの下で提供されています。詳細は [LICENSE](LICENSE) ファイルを参照してください。
