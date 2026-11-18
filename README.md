# mcp-gateway

One MCP server in front of several upstream MCP servers. It keeps its own tool catalog tiny (3 tools),
connects to an upstream only when that upstream is used, and exposes upstream health to both the agent
and an OpenCode TUI badge.

Design: [`docs/00_basic_design.md`](docs/00_basic_design.md) · TUI plugin: [`docs/01_tui_design.md`](docs/01_tui_design.md)

## Why

Every connected MCP server costs its whole `tools/list` payload on every model request. Measured on
`redmine-mcp`: 9 tools = 9,238 bytes ≈ 2.4k tokens, before instructions. The gateway replaces that with
three tools (< 1.5 KB, enforced by a test), and an upstream's schema is only fetched when that upstream
is actually used.

## Build and test

```bash
go build ./...
go test ./...                # unit tests, no network
go vet ./...
gofmt -w .

go test ./internal/gateway -run TestCatalogBudget -v   # the token-budget guard

cd plugin
npm install
npm test                     # node --test, pure modules
npm run typecheck            # tsc --noEmit against @opencode/plugin 2.3.0
```

Live test against a running `redmine-mcp` (not part of CI):

```bash
./scripts/functional.sh /path/to/redmine-mcp.env
```

It builds the binary, starts it on a free port, points it at the upstream from that env file, and
verifies list/call/deny/status through the real HTTP transport.

## Configure

Copy [`config.example.json`](config.example.json) to `~/.config/mcp-gateway/config.json` (mode 600).
`${VAR}` is expanded from the environment; an unset variable is a startup error.

| Field | Meaning |
|---|---|
| `listen` | comma-separated `host:port` list; a non-loopback address requires `token` |
| `path` | MCP endpoint, default `/mcp` |
| `token` | bearer token for the MCP endpoint (empty only on loopback) |
| `status_token` | bearer token for `GET /status`; empty disables that endpoint |
| `idle_timeout` | close an upstream after this long unused (default `60s`) |
| `connect_timeout` | per-connect deadline (default `10s`) |
| `call_timeout` | per-tool-call deadline (default `120s`) |
| `schema_ttl` | tool-catalog cache lifetime (default `5m`) |
| `servers.<name>` | `http` (`url`, `headers`) or `stdio` (`command`, `env`, `cwd`) |
| `allow` / `deny` | tool name lists (`*` supported); `deny` wins and is checked before connecting |

## Run

```bash
mcp-gateway --config ~/.config/mcp-gateway/config.json             # HTTP
mcp-gateway --config ~/.config/mcp-gateway/config.json --stdio     # stdio
```

`GET /status` (bearer `status_token`) returns:

```json
{"generated_at":"2026-11-17T12:15:44Z","servers":[
  {"name":"redmine","transport":"http","state":"up","tools":9,"latency_ms":31,
   "last_ok":"2026-11-17T12:15:40Z","last_error":null,"retry_in_s":0}]}
```

`state` is `up` (live session), `idle` (used before, now closed), `down` (last attempt failed) or
`unknown` (never used). Reading status never opens a connection.

## Tools

| Tool | Purpose |
|---|---|
| `list_servers` | state, tool count, latency, last error per upstream |
| `list_tools` | compact index (`server`, `q`, `full`); `full=true` returns the JSON Schema |
| `call_tool` | `server`, `tool`, `arguments` — arguments pass through unchanged |

A result above 256 KiB is truncated with a visible marker. Upstream `IsError` results keep their text
and gain a `[server/tool]` prefix.

## TUI plugin

`plugin/` renders upstream health in OpenCode 2.x. Local paths are valid in `cli.json`
(documented: `"/abs/path"` or `"file:///abs/path"`):

```json
{
  "plugins": [
    {
      "package": "/home/jinchk/.config/opencode.plugins/mcp-gateway-status",
      "options": {
        "url": "http://127.0.0.1:8930",
        "token": "<status_token>",
        "intervalMs": 5000
      }
    }
  ]
}
```

The token lives in `cli.json` (mode 600), not in the environment. Environment variables
(`MCP_GATEWAY_STATUS_URL`, `MCP_GATEWAY_STATUS_TOKEN`, `MCP_GATEWAY_STATUS_INTERVAL`) still work as a
fallback and are useful for a one-off test.

The footer shows `GW 2/3` (up/total), `GW ?` after three failed polls, `GW !` when the token is missing
and `GW …` while loading. The sidebar lists each upstream; `/gateway-status` opens a detail dialog with
`last_error`. Configuration precedence: plugin `options` → environment → plugin storage (URL only) → default.

## Security

- Upstream credentials live in the config file or its environment; nothing is written back.
- Two tokens: `token` for MCP, `status_token` for `/status` (health is internal).
- `/status` never includes credentials; tool arguments are not logged at info level.
- A gateway makes client-side per-tool permissions impossible (there is only `gateway_call_tool`), so
  put the policy in `allow`/`deny` and keep upstream servers' own read-only modes on.

## License

MIT — see [LICENSE](LICENSE).
