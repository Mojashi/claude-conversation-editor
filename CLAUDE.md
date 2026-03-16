# Surgery - Claude Code Conversation Editor

## Build

This is a Wails application. **Do NOT use `go build`** — it will produce a binary that silently fails to open windows.

```bash
wails build
```

The output binary is at `build/bin/Surgery.app/Contents/MacOS/surgery`.

`~/bin/surgery` is symlinked to this binary for CLI use.
