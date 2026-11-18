# AGENTS.md

MCP gateway (Go) fronting several upstream MCP servers. Design: `docs/00_basic_design.md`; TUI plugin:
`docs/01_tui_design.md`. Usage: `README.md`.

## Commands

```bash
go build ./... && go test ./... && go vet ./... && gofmt -w .

go test ./internal/gateway -run TestCatalogBudget -v     # token-budget guard
go test ./internal/registry -run TestConcurrent -v      # single-flight
cd plugin && npm test && npm run typecheck

./scripts/functional.sh /path/to/redmine-mcp.env     # live, needs the upstream service
```

`go.mod` pins `go 1.26.4`; only dependency is `github.com/modelcontextprotocol/go-sdk`. CI runs
`gofmt` → `vet` → `test -race`, plus the plugin's `npm test` and `tsc`.

## MCP rules that are easy to break

- **stdout is the JSON-RPC wire under `--stdio`.** Logs go to stderr through `slog`; never print to stdout.
- The gateway's catalog is three tools on purpose. `TestCatalogBudget` fails above 1,536 bytes — if you
  add a tool or lengthen descriptions, you are removing the reason this project exists.
- `call_tool` arguments are passed as `map[string]any` and forwarded unchanged. Never mirror or retype an
  upstream schema in the gateway; upstreams we have not seen would break.
- Upstream results are opaque: text stays text, images stay images. Only `IsError` results get a
  `[server/tool]` prefix, and only results over 256 KiB are cut.
- HTTP upstreams need the configured `headers` on **every** request (`headerRoundTripper`); dropping them
  reproduces the "initialize: Unauthorized" bug the functional test caught.

## Registry invariants

- At most one session per upstream; `dialMu` serialises connects so concurrent calls share one attempt.
- `inflight` counts callers holding a session; the sweeper only closes when it is zero. Do not close a
  session outside that check.
- Any error from `CallTool` is a transport error (tool errors come back as `IsError`), so it drops the
  session and arms the 5s→60s backoff.
- `deny` is checked in `call_tool` before `registry.Call`, so a denied tool never opens a connection.
- `/status` must stay network-free; it reads `Registry.Snapshot()` only.
- `POST /upload/<server>` forwards to the upstream's `/upload` with the configured `headers`; it uses
  the MCP token, the `max_upload` cap, and requires one of the upstream's `upload_requires_any` tools to
  be allowed. It must never expose upstream credentials to the client, and it must not assume a
  particular upstream tool set.

## Configuration

- `config.json` is never committed; `config.example.json` is. Secrets come from `${ENV}`; an unset
  variable fails startup.
- Unknown JSON fields are rejected on purpose — a typo should fail at startup, not silently disable a
  timeout.

## Plugin

- Pure logic lives in `src/status.ts`, `src/config.ts`, `src/poller.ts` and is tested with
  `node --test`; `src/tui.tsx` only wires slots/signals to it. Keep it that way so tests stay runnable
  without OpenCode.
- The token comes from cli.json plugin `options` (primary) or `MCP_GATEWAY_STATUS_TOKEN` (fallback). Never
  put it in plugin storage, logs, or a rendered string.
- The plugin talks to `GET /status`, never to MCP tools.
- Local loading in `cli.json` accepts absolute paths or `file:///…`; this is verified, not inferred.
- `~/.config/opencode.plugins/mcp-gateway-status/` is an installed **copy**; refresh it after plugin
  changes with `cp plugin/package.json plugin/src/*.{ts,tsx,d.ts} …/src/` (or recopy the tree).

## Tests

- Unit tests use fake sessions and an injected dialer; no network.
- `test/functional_test.go` is behind `//go:build functional` and requires `REDMINE_MCP_URL` +
  `REDMINE_MCP_TOKEN`; it is not part of CI.
- Status/summary formatting and the catalog budget are the regression surface for token cost.
