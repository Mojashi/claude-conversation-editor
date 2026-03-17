# Surgery - Claude Code Conversation Editor

## Build

This is a Wails application. **Do NOT use `go build`** — it will produce a binary that silently fails to open windows.

```bash
wails build
```

The output binary is at `build/bin/Surgery.app/Contents/MacOS/surgery`.

`~/bin/surgery` is symlinked to this binary for CLI use.

## Testing

```bash
go test -v ./...
```

## API Intercept

Proxy to capture actual API requests sent by Claude Code:

```bash
# Terminal 1: start proxy
bun run scripts/intercept.ts 18888

# Terminal 2: run Claude Code through proxy
ANTHROPIC_BASE_URL=http://localhost:18888 claude -r <session-id> -p "hello"

# Compare two captures (e.g. original vs compact)
python3 scripts/compare-requests.py original:/tmp/intercept-18889-2.json compact:/tmp/intercept-18888-2.json
```

Requests are saved to `/tmp/intercept-<port>-<n>.json`. The first request (#1) is usually system prompt only; #2 is the main conversation.
