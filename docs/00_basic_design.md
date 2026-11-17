# mcp-gateway — Basic Design

| | |
|---|---|
| Status | Draft, ready to implement |
| Purpose | One MCP server in front of N upstream MCP servers |
| Language | Go **1.26.4**, MCP SDK `github.com/modelcontextprotocol/go-sdk` **v1.8.0** |
| Transports | stdio and Streamable HTTP (same as `redmine-mcp`) |
| UI | OpenCode TUI plugin (TypeScript, `@opencode/plugin/tui`) |
| Relation | Sibling project to `redmine-mcp`; shares its config/transport patterns |

---

## 1. Problem and goals

OpenCode pays for every connected MCP server twice: a connection at startup, and the whole
`tools/list` catalog on every model request. Measurements from `redmine-mcp`:

| Measurement | Value |
|---|---|
| 9 tools, `tools/list` JSON | **9,238 bytes** ≈ 2.4k tokens |
| Server instructions | ~800 bytes ≈ 0.2k tokens |
| One `search_issues` page (25 issues) | 2,527 chars ≈ 0.99k tokens (raw JSON: 28,622 chars) |

With six servers connected, 10k–20k tokens of catalog are injected per request. `mcp-gateway`
collapses that to one small catalog and pays an upstream's schema only when that upstream is
actually used.

### Goals

1. One MCP server whose own catalog is tiny (target < 1.5 KB for all of its tools).
2. Lazy upstream connections: connect on first use, disconnect after an idle timeout.
3. Full fidelity for tool calls: arguments and results pass through untouched.
4. Per-upstream status visible to the agent (a tool) and to the human (a TUI panel).
5. Per-upstream allow/deny enforced by the gateway, replacing the client-side permissions that a
   gateway makes impossible.

### Non-goals (v1)

- Proxying MCP **resources**, **prompts**, **sampling**, **elicitation** or progress notifications.
  Only `tools/list` and `tools/call` are forwarded.
- Cross-server transactions, retries of write calls, or query fan-out.
- Being a security boundary between upstreams: one process holds all upstream credentials.
- Hot reload of upstream config; changing it requires a restart.
- Replacing the upstream servers. They keep running exactly as today.

---

## 2. Architecture

```
cmd/mcp-gateway/main.go        config, MCP server, HTTP/stdio, /status endpoint
internal/config/               JSON config, ${ENV} expansion, validation
internal/registry/             upstream table, connections, single-flight, idle close
internal/gateway/              the three tools; schema cache; result passthrough
internal/status/               status snapshot + HTTP handler
plugin/                        OpenCode TUI plugin (TypeScript, separate package)
docs/00_basic_design.md        this file
```

Boundaries:

- `internal/registry` knows nothing about MCP tool schemas; it owns connections and calls.
- `internal/gateway` translates MCP requests into registry calls; it is the only package that
  imports `mcp`.
- `internal/status` is read-only over the registry snapshot and imports neither MCP nor tools.
- The plugin talks to `GET /status` over HTTP; it never speaks MCP.

The gateway is a **normal MCP server** from OpenCode's perspective. It is configured once, next to
the existing servers, and the upstreams disappear from `opencode` config entirely.

---

## 3. Tool surface (normative)

Three tools. Every byte here is paid on every request, so the descriptions stay short and the
schemas stay flat.

| Tool | Input | Output |
|---|---|---|
| `list_servers` | — | one line per upstream: `name state tools latency last_error` |
| `list_tools` | `server`, optional `q`, `full` | compact index by default; full JSON Schema for one server when `full: true` |
| `call_tool` | `server`, `tool`, `arguments` (object) | the upstream result, unchanged |

### `list_servers`

Doubles as the agent-facing health tool:

```
# server | transport | state | tools | latency_ms | last_error
redmine | http | up | 9 | 12 | -
reviewboard | http | down | - | - | connect: dial tcp ... refused (retry in 3s)
```

States: `up`, `idle` (connected before, currently closed), `down`, `unknown` (never used).
`list_servers` never touches the network; it reads the registry snapshot, so a never-used
upstream stays `unknown` until something calls it.

### `list_tools`

- Default (`full: false`): ≤ 1 line per tool — `name — first sentence (args: project?, limit?)`.
  Enough for the model to pick a tool without loading every schema.
- `full: true`: the upstream's `inputSchema` verbatim, for the tools matching `q` or all of them.
  This is the only place an upstream's 2.4 KB schema is paid, and only when that server is used.
- The index is cached per upstream for `schema_ttl` (default 5m), invalidated on
  `notifications/tools/list_changed`.

### `call_tool`

```json
{ "server": "redmine", "tool": "search_issues", "arguments": { "project": "web", "limit": 5 } }
```

- `arguments` is passed through as `json.RawMessage`, never re-typed by the gateway. A struct with
  a fixed shape would break every upstream schema we have not seen.
- The upstream's `CallToolResult.Content` is returned as the gateway's own content. Text stays
  text; images and embedded resources are forwarded as-is.
- `IsError` from the upstream is propagated, with a `[server/tool]` prefix so the model can tell
  which side failed.
- A result larger than 256 KiB is truncated with an explicit `… (truncated, N bytes total)` marker.
  Nothing else is trimmed: upstream servers already format their output.

### Why not name tools `<server>_<tool>` and expose them all?

That is the "proxy-all" variant. It keeps per-tool client permissions but pays every upstream
catalog on every request, i.e. it saves nothing. It is only worth adding later as an opt-in
`mirror: true` per server for a hot upstream used in every turn.

### Explicitly lost

Client-side permissions like `{"action":"redmine_sh_update_issue","effect":"deny"}` no longer
exist; there is only `gateway_call_tool`. The gateway therefore owns enforcement:

```json
{ "allow": ["search_issues", "get_issue"], "deny": ["update_issue", "create_issue", "log_time"] }
```

`deny` wins over `allow`; an empty `allow` means "everything not denied". Denial is checked before
connecting, so a blocked call never opens a connection.

---

## 4. Upstream configuration

One JSON file, `~/.config/mcp-gateway/config.json`, mode 600. `${VAR}` is expanded from the
environment so keys can stay out of the file.

```json
{
  "listen": ["127.0.0.1:8930"],
  "path": "/mcp",
  "token": "${MCP_GATEWAY_TOKEN}",
  "status_token": "${MCP_GATEWAY_STATUS_TOKEN}",
  "idle_timeout": "60s",
  "connect_timeout": "10s",
  "call_timeout": "120s",
  "schema_ttl": "5m",
  "servers": {
    "redmine": {
      "transport": "http",
      "url": "http://127.0.0.1:8080/mcp",
      "headers": { "Authorization": "Bearer ${REDMINE_MCP_TOKEN}" }
    },
    "redmine-sh": {
      "transport": "http",
      "url": "http://127.0.0.1:8081/mcp",
      "headers": { "Authorization": "Bearer ${REDMINE_SH_MCP_TOKEN}" },
      "allow": ["search_issues", "get_issue", "list_projects", "list_metadata"]
    },
    "reviewboard": {
      "transport": "stdio",
      "command": ["/usr/local/bin/reviewboard-mcp"],
      "env": { "REVIEWBOARD_TOKEN": "${RB_TOKEN}" },
      "cwd": "/path/to/reviewboard-mcp"
    }
  }
}
```

Rules:

- `listen` accepts the same comma-separated form as `redmine-mcp`; every address is bound before
  serving, and a non-loopback address requires `token`.
- A `stdio` upstream is spawned on demand and killed after `idle_timeout`; its stderr is copied to
  the gateway log with a `[server]` prefix.
- Unknown fields are rejected (`DisallowUnknownFields`) so typos fail at startup, not at 3 a.m.
- A config error aborts startup. An unreachable upstream does not: it becomes `down` in status.

---

## 5. Connection lifecycle

Per upstream, keyed by name:

1. `call_tool` / `list_tools(full)` asks the registry for a live session.
2. If none and no failure backoff is active: connect (`connect_timeout`), send `initialize`, then
   `notifications/initialized`. Connect attempts are **single-flight** — concurrent callers wait on
   the same attempt instead of spawning two `stdio` processes.
3. On success: store the session, record `last_ok`, schedule idle close.
4. On failure: record the error and a backoff deadline (5s, doubled to a 60s cap). Callers get an
   actionable error immediately; they do not wait for a retry.
5. Idle timer: close the session after `idle_timeout` with no calls. `list_tools` cache survives.

Guarantees:

- At most one session per upstream at any time.
- No goroutine or child process outlives its session: close cancels the transport and waits.
- Shutdown (SIGTERM) closes every session before exit.
- A dead upstream never blocks calls to healthy ones; each call has its own `call_timeout`.

---

## 6. Status HTTP endpoint

Same listener as MCP, separate path, separate read-only bearer token (the TUI plugin needs one and
must not be given the MCP token).

```
GET /status
Authorization: Bearer <status_token>
```

```json
{
  "generated_at": "2026-11-17T11:20:03Z",
  "servers": [
    { "name": "redmine", "transport": "http", "state": "up", "tools": 9,
      "latency_ms": 12, "last_ok": "2026-11-17T11:19:58Z", "last_error": null,
      "retry_in_s": 0 },
    { "name": "reviewboard", "transport": "http", "state": "down", "tools": 0,
      "latency_ms": null, "last_ok": null,
      "last_error": "connect: dial tcp 127.0.0.1:8910: connect: connection refused" }
  ]
}
```

- `state` is computed, not polled: `up` means a live session or a successful call within
  `idle_timeout`; `idle` means a successful call older than that but no failure; `down` means the
  last attempt failed.
- Reading `/status` never opens a connection. A separate `?probe=1` (optional, off by default)
  may ping, for manual debugging.
- Errors are strings, already redacted (§8): no headers, no tokens, no request bodies.

---

## 7. TUI plugin

A small package, `plugin/`, exporting `./tui`. It renders upstream status; it never calls tools.

| Slot | Content |
|---|---|
| `home.footer.status` | `GW 3/4` — up/total, one token wide |
| `sidebar.content` | one line per upstream: `● redmine 9 tools 12ms`, `○ reviewboard down` |

- Implemented with `context.ui.slot({ append: "home.footer.status", render })` and the
  `sidebar.content` slot, both documented for V2 TUI plugins.
- Polls `/status` every 5s with a 2s timeout, using `context.storage.store` for the URL and token.
  After three consecutive failures it shows `GW ?` and keeps the last data greyed out.
- `/gateway-status` command opens a dialog with the full table plus `last_error` text.
- Colors come from `context.theme`, never hard-coded.
- No token ever rendered, logged, or stored in the session transcript.
- Loading: package installed under `~/.config/opencode/plugins/` and listed in `cli.json`
  `plugins`; the executable path/URL is configured through plugin options.
  **Open question:** confirm whether `cli.json` accepts a local path entry or requires a package
  name; verify before writing the loader instructions.

---

## 8. Security

- Upstream credentials live only in the config file (600) or its environment; `${VAR}` is resolved
  at startup and never written back.
- Two tokens: `token` for MCP, `status_token` for `/status`. Both compared in constant time.
- `/status` never includes URLs with credentials, headers, or tool arguments.
- `list_tools(full)` returns upstream schemas, which are public API surface; `call_tool` never logs
  `arguments` at info level (debug only, and then redacted by key name matching
  `token|key|secret|password|authorization`).
- Deny rules are checked before connecting, so a disallowed tool cannot even be probed.
- The gateway trusts upstream `IsError`/annotations; it does not try to decide what is destructive.
  Write protection is the deny list plus each upstream's own read-only mode.

---

## 9. Token budget

Target, measured the same way as the baseline:

| Item | Budget |
|---|---|
| Gateway `tools/list` JSON (3 tools) | < 1,536 bytes |
| Gateway instructions | < 512 bytes |
| Total per request | ~0.5k tokens |

Baseline to beat: two `redmine-mcp` servers alone are 2 × 9,238 B ≈ 4.8k tokens per request, before
instructions. A regression test asserts the 1,536-byte ceiling so tool descriptions cannot grow
silently.

The gateway's own result sizes are the upstream's; no extra formatting is added beyond the
`[server/tool]` prefix on errors.

---

## 10. Failure modes

| Failure | Behaviour |
|---|---|
| Upstream down at call time | Error with the underlying cause + retry hint; `list_servers` shows `down`; other upstreams unaffected |
| Upstream hangs | `call_timeout` cancels the call; the session is closed and re-created on next use |
| stdio child dies | Session close is detected; next call reconnects (subject to backoff) |
| Bad config entry | Startup fails with the field path, before any listener binds |
| `/status` unreachable | Plugin degrades to `GW ?`; MCP traffic unaffected |
| Huge upstream result | Truncated at 256 KiB with a visible marker |
| Unknown tool name | `call_tool` returns the upstream's error unchanged, prefixed |

---

## 11. Testing

- **Registry**: single-flight (two concurrent connects produce one session), idle close, backoff
  after failure, shutdown closes everything. Fake upstreams via the SDK's in-memory transports for
  speed, plus one real `stdio` child for process lifecycle.
- **Tools**: passthrough fidelity — arguments arrive byte-identical, text/image content survives,
  `IsError` propagates, truncation marker appears above the cap.
- **Config**: `${VAR}` expansion, unknown-field rejection, comma-separated `listen`, non-loopback
  requires a token, deny-before-connect.
- **Status**: JSON shape, token required, no secrets in errors.
- **Catalog budget**: assert `tools/list` JSON < 1,536 bytes (the regression that protects the
  whole point of the project).
- **Plugin**: pure formatting/polling functions unit-tested; rendering verified manually against a
  live gateway (no snapshot harness worth building for two slots).

---

## 12. Milestones

1. **M1** — config, registry, three tools, stdio + HTTP transports. Gateway usable against
   `redmine` and `redmine-sh`; measure the catalog and the first real savings.
2. **M2** — `GET /status`, TUI plugin, `/gateway-status` dialog.
3. **M3 (optional)** — `mirror: true` per server for hot upstreams; progress forwarding; resource
   passthrough. Only if M1/M2 measurements ask for it.

---

## 13. When not to build this

Do not deploy the gateway if any of these hold:

- Fewer than ~4 MCP servers, all used daily. Manual `/mcps` toggling or per-project configs save
  the same tokens with zero moving parts.
- Per-tool client permissions are load-bearing. The gateway downgrades them to a single
  `gateway_call_tool` action; its allow/deny list is a real substitute but lives in another process
  and another file.
- Clients need MCP resources, prompts or progress. v1 does not forward them.

---

## 14. Open questions

1. ~~Local TUI plugin loading~~ — resolved: OpenCode 2.3.0 accepts an absolute path in `cli.json`,
   verified by loading the plugin in a standalone TUI.
2. Whether `list_tools(full)` should include upstream annotations (`readOnlyHint`), which would let
   the model avoid write tools without guessing from names.
3. Whether idle-closing `stdio` upstreams is worth it for a binary that starts in 50 ms — measure
   spawn cost before adding complexity.
4. Distribution of `status_token` to the plugin: environment substitution versus a plugin-local
   settings file.
