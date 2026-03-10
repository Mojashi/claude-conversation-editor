# Surgery ⚡

**コンテキストエンジニアリングのための Claude Code 会話エディタ**

Claude Code の会話履歴（JSONL）を編集するネイティブアプリ。長くなったセッションから不要なやり取りを切り落とし、コンテキストを意図的に設計することでモデルのパフォーマンスを最大化する。

## インストール

```sh
curl -fsSL https://raw.githubusercontent.com/Mojashi/claude-conversation-editor/master/install.sh | sh
```

`~/bin` に PATH が通っていない場合は `SURGERY_BIN_DIR` で指定:

```sh
curl -fsSL https://raw.githubusercontent.com/Mojashi/claude-conversation-editor/master/install.sh | SURGERY_BIN_DIR=/usr/local/bin sh
```

<details>
<summary>手動インストール</summary>

[Releases](https://github.com/Mojashi/claude-conversation-editor/releases) から最新の `surgery-darwin-arm64.zip` をダウンロードして展開。

```bash
cp -r surgery.app /Applications/
ln -sf /Applications/surgery.app/Contents/MacOS/surgery ~/bin/surgery
```
</details>

## 使い方

### Claude Code から（推奨）

Claude Code のセッション内で `!surgery` を実行:

```
!surgery
```

`!` プレフィックスでシェルコマンドとして直接実行される。`CLAUDECODE=1` 環境変数を検出し、現在のセッション JSONL をトークンで特定してウィンドウを開く。

### ターミナルから

```bash
surgery
```

カレントディレクトリのプロジェクトを開く。セッションは一覧から選択。

### 操作

| 操作 | 説明 |
|------|------|
| チェックボックス | 削除対象を選択 |
| Shift + クリック | 範囲選択 |
| ✂ Truncate after | そのメッセージ以降を全選択 |
| Tools / Sidechain | tool_use・サイドチェーンの表示切替 |
| Delete Selected | 選択をプレビュー |
| Save | JSONL に書き込み（`.jsonl.bak` に自動バックアップ） |

## 自動アップデート

起動時に新バージョンがあればヘッダーに通知が出る。クリックで自動ダウンロード＆再起動。

## ビルド

```bash
# 依存: Go 1.21+, Node 18+, Wails v2
go install github.com/wailsapp/wails/v2/cmd/wails@latest
wails build
```

## License

MIT
