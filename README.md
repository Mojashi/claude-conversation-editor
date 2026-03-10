# Surgery ⚡

**コンテキストエンジニアリングのための Claude Code 会話エディタ**

Claude Code の会話履歴（JSONL）を編集するネイティブアプリ。長くなったセッションから不要なやり取りを切り落とし、コンテキストを意図的に設計することでモデルのパフォーマンスを最大化する。

## インストール

```sh
curl -fsSL https://raw.githubusercontent.com/Mojashi/claude-conversation-editor/master/install.sh | sh
```

`~/bin` にPATHが通っていない場合は `SURGERY_BIN_DIR` で指定:

```sh
curl -fsSL https://raw.githubusercontent.com/Mojashi/claude-conversation-editor/master/install.sh | SURGERY_BIN_DIR=/usr/local/bin sh
```

### 手動

[Releases](https://github.com/Mojashi/claude-conversation-editor/releases) から最新の `surgery-darwin-arm64.zip`（Apple Silicon）または `surgery-darwin-amd64.zip`（Intel）をダウンロードして展開。

```bash
# アプリをインストール
cp -r surgery.app /Applications/

# surgery コマンドを PATH に追加（バイナリに直接リンク）
ln -sf /Applications/surgery.app/Contents/MacOS/surgery ~/bin/surgery
```

## 使い方

Claude Code のセッション内から:

```bash
surgery
```

1. ランダムトークンを出力して即終了
2. バックグラウンドで現在のセッション JSONL を特定
3. Surgery ウィンドウが開き、当該セッションにフォーカス

### 操作

- **チェックボックス**: 削除対象のメッセージを選択
- **Shift+クリック**: 範囲選択
- **✂ Truncate after**: そのメッセージ以降を全選択
- **Tools / Sidechain**: tool_use・サイドチェーンの表示切替
- **Delete Selected**: 選択をプレビュー
- **Save**: JSONL に書き込み（`.jsonl.bak` に自動バックアップ）

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
