# TUI Plugin — Design

Companion to [`00_basic_design.md`](00_basic_design.md) §7. This file is implementation-grade: package
layout, configuration, polling, rendering, failure states, loading and tests.

| | |
|---|---|
| Runtime | OpenCode **2.3.0** TUI plugin API (`@opencode/plugin/tui`) |
| Language | TypeScript / TSX, executed by OpenCode's runtime — **no build step** |
| Host toolchain | Node **24** (for tests only; no Bun on the host) |
| Location in repo | `plugin/` (private, not published to npm) |
| Talks to | `GET /status` on the gateway, HTTP + bearer token |
| Never does | call MCP tools, read the gateway config, log the token |

---

## 1. What it shows

Three surfaces, in order of importance:

| Surface | Slot | Content | Update |
|---|---|---|---|
| Footer badge | `home.footer.status` | `GW 2/3` — one short string, fits next to the version | every poll |
| Sidebar list | `sidebar.content` | one line per upstream: `● redmine 9t 12ms` / `○ reviewboard down` | every poll |
| Details dialog | `/gateway-status` | full table incl. `last_error`, last-update age | on invoke |

Rules:

- The footer badge must never grow beyond ~10 characters; anything longer belongs in the dialog.
- The sidebar shows at most 8 upstreams; the rest collapse into `+3 more` (open the dialog for all).
- The dialog is the only place `last_error` is rendered, truncated to 120 chars.
- State glyphs: `●` up, `◌` idle, `○` down, `?` unknown/stale. Colors come from `context.theme`
  (`text.success`, `text.warning`, `text.error`, `text.muted`); never hard-coded hex.

---

## 2. Package layout

```
plugin/
  package.json
  tsconfig.json
  src/
    tui.tsx          # Plugin.define, slots, dialog, command, poll lifecycle
    poller.ts        # fetch loop, abort, single-flight, backoff (fetch injected)
    status.ts        # pure: types, parse, summarize, row/format functions
    config.ts        # environment + storage resolution, URL validation
    globals.d.ts     # the one global (process.env) the plugin uses
    status.test.ts   # node --test, no OpenCode runtime needed
    config.test.ts
    poller.test.ts
```

`package.json` essentials:

```json
{
  "name": "opencode-mcp-gateway-status",
  "private": true,
  "type": "module",
  "exports": { "./tui": "./src/tui.tsx" },
  "scripts": { "test": "node --test src/*.test.ts" },
  "dependencies": { "@opencode/plugin": "2.3.0" },
  "peerDependencies": {
    "solid-js": ">=1.9.0",
    "@opentui/core": ">=0.6.6",
    "@opentui/solid": ">=0.6.6"
  }
}
```

The plugin has no server part and no GUI part: only `./tui`. OpenCode resolves the
`@opencode/plugin/tui` import at runtime, so the package is never bundled by us.

`status.ts` and `config.ts` must stay **plain erasable TypeScript** (no enums, no decorators,
no `namespace`) so Node 24 can execute them directly for tests — no `tsc` in the loop. If Node
refuses a syntax, the fallback is `npx tsc --noEmit` for types plus `node --experimental-strip-types`.

---

## 3. Configuration

Precedence, highest first:

1. `MCP_GATEWAY_STATUS_URL` / `MCP_GATEWAY_STATUS_TOKEN` from the plugin process environment.
2. `context.storage.store("config", …)` — user-editable from the dialog's "Settings" action; no token
   here, URL only.
3. Default `http://127.0.0.1:8930`.

`context.options` is not used: the `cli.json` `plugins` entry is a string list with no options
schema we can rely on (see §8). The environment route works for local and SSH-forwarded setups and
keeps the token out of every JSON file.

Validation:

- URL must parse, scheme `http`/`https`, no query/fragment (append `/status`).
- Token must be non-empty; if missing, the plugin still loads and shows `GW !`, and the dialog
  explains exactly which env var to set. It never prompts for a token in the UI.
- Poll interval: `MCP_GATEWAY_STATUS_INTERVAL` ms, default 5000, clamped 1000–60000.
- Request timeout: 2000 ms, fixed.

---

## 4. Status client

`status.ts` exposes pure functions; `tui.tsx` owns timers.

```ts
export type Upstream = {
  name: string; transport: "http" | "stdio"; state: "up" | "idle" | "down" | "unknown";
  tools: number; latency_ms: number | null; last_ok: string | null; last_error: string | null;
};
export type Snapshot = { generated_at: string; servers: Upstream[] };

parseStatus(body: unknown): Snapshot            // throws on shape mismatch
summarize(s: Snapshot): { up: number; total: number; badge: string; tone: Tone }
rows(s: Snapshot, limit = 8): Row[]             // name, glyph, detail, tone
describeError(err: unknown): string             // short, token-free
```

Polling contract:

- `setInterval`-style loop with `AbortController` per request; **no overlapping requests** — the
  next tick is scheduled after the current one settles.
- Backoff on failure: 5s → 10s → 20s → 40s → 60s (cap). Reset to the normal interval on success.
- A single 5xx/timeout does **not** immediately flip the UI to `?`; three consecutive failures do.
  Until then the last good snapshot stays, dimmed.
- Success updates `lastGoodAt`; the dialog renders `updated 12s ago` from it.
- `stop()` clears the timer and aborts an in-flight request; called from the cleanup function
  returned by `setup`.

No caching on disk: the snapshot is memory-only. It is display data, not truth.

---

## 5. Rendering

```tsx
export default Plugin.define({
  id: "mcp.gateway.status",
  setup(context) {
    const cfg = resolveConfig(context)                    // env + storage
    const [snap, setSnap] = createSignal<Snapshot | null>(null)
    const [stale, setStale] = createSignal(false)
    const [failures, setFailures] = createSignal(0)

    const poller = createPoller({ ...cfg, onSnapshot: setSnap, onStale: setStale, onFailures: setFailures })
    poller.start()
    if (!cfg.token) { /* keep GW ! */ } else { poller.refresh() }

    const badges = {
      append: "home.footer.status" as const,
      render: () => <text fg={toneColor(context, stale() ? "muted" : summary().tone)}>{summary().badge}</text>,
    }

    const offFooter = context.ui.slot(badges)
    const offSidebar = context.ui.slot({
      append: "sidebar.content",
      render: () => <UpstreamList ... />,
    })

    context.keymap.layer(() => ({
      mode: "global",
      commands: [{
        id: "mcp.gateway.status.show",
        title: "Gateway status",
        group: "MCP Gateway",
        palette: true,
        slash: { name: "gateway-status" },
        run: () => context.ui.dialog.show(() => <StatusDialog ... />),
      }],
    }))

    return () => { poller.stop(); offFooter(); offSidebar() }
  },
})
```

Notes:

- The footer render must be a single `<text>`; any layout wrapper breaks the built-in footer row.
- Use `context.theme` tokens, never literals, so themes keep working.
- `sidebar.content` is a list slot: return a `<box flexDirection="column">` with one `<text>` per
  upstream; do not take over the slot with `replace`.
- Dialog content is plain JSX (`box`/`text`), monospace-aligned by padded names; no markdown
  renderer needed.
- Keep the render tree cheap: the component reads signals, so a poll re-renders only the
  contributions, not the whole app.

---

## 6. Failure and stale states

| Condition | Footer | Sidebar | Dialog |
|---|---|---|---|
| No token configured | `GW !` | `set MCP_GATEWAY_STATUS_TOKEN` | same + env var name |
| First load in flight | `GW …` | `loading` | — |
| 1–2 failed polls, last snapshot exists | `GW 2/3` dimmed | rows dimmed, right side `stale` | `updated 22s ago` |
| ≥3 failed polls / no snapshot | `GW ?` | `gateway unreachable` | full error text, `Retry` action |
| Gateway up, one upstream down | `GW 2/3` warning tone | `○ reviewboard down` | error text for that upstream |
| All down | `GW 0/3` error tone | all `○` | errors per row |

The plugin never blocks the TUI, never throws from `render`, and never retries in a tight loop.
Any unexpected error inside `setup` is caught and turned into the `GW !` state with the error
message available in the dialog.

---

## 7. Tests

Unit (`node --test`, no OpenCode runtime, no network):

- `parseStatus`: valid payload; missing field; wrong types; empty `servers`; extra fields ignored.
- `summarize`: all up / mixed / all down / empty; badge never exceeds 10 chars.
- `rows`: truncation to 8, name padding, error rows, `idle` glyph.
- `describeError`: strips token if it appears in a message (must, defensively); caps length.
- `resolveConfig`: env precedence, URL validation (reject `file:`, empty host), interval clamping,
  missing token path.
- `createPoller` with an injected fake `fetch`: success → snapshot; 500 three times → stale; backoff
  schedule; `stop()` aborts and does not call `onSnapshot` afterwards.

Integration (scripted, runs against the real gateway):

1. start `mcp-gateway` with one upstream (`redmine-mcp-neo`) and a `status_token`;
2. `GET /status` with and without the token (200 / 401);
3. feed the JSON through `parseStatus` + `summarize` and assert `GW 1/1`;
4. stop the gateway and assert three failures flip the computed state to `?`.

Visual check (manual, cannot be automated): load the plugin, see `GW 1/1` in the footer, open
`/gateway-status`, stop the gateway, watch the badge become `?` within ~15s.

---

## 8. Loading and install

**Verified** (OpenCode 2.3.0): `cli.json` accepts an absolute path or `file:///…` for a local package,
and the plugin must expose `./tui` through `package.json` `exports`:

```json
{ "plugins": ["/path/to/mcp-gateway/plugin"] }
```

A real standalone TUI was launched with the plugin and rendered `GW !` (no token) and `GW 0/1` (live
gateway), confirming load, poll, parse and render. `cli.json` also accepts the object form
`{ "package": "…", "options": { … } }`; this plugin keeps its URL and token in the environment instead,
so options stay unused.

Security: the plugin runs in the TUI process and inherits its environment. The token therefore
must come from the environment, never from `cli.json` (which may be shared or synced) and never
from `context.storage` (which is durable and visible in the UI).

---

## 9. Remote TUI

When the TUI connects to a remote OpenCode server, the plugin still runs **on the client machine**.
The gateway URL must therefore be reachable from the client, not only from the server:

- same machine: default `http://127.0.0.1:8930` works;
- SSH use: forward the status port too (`ssh -L 8930:127.0.0.1:8930 …`) or point the URL at the
  forwarded port;
- never put a public URL in the default: the status endpoint carries upstream health, which is
  internal information.

---

## 10. Work list

1. `plugin/package.json`, `tsconfig.json`, `src/status.ts`, `src/config.ts`.
2. `src/tui.tsx` with footer slot, sidebar slot, command + dialog, poller lifecycle.
3. Unit tests for `status.ts` / `config.ts`; `npm test` green.
4. `npx tsc --noEmit` clean (types from `@opencode/plugin@2.3.0`).
5. Gateway side: implement `/status` exactly as in `00_basic_design.md` §6 before wiring the UI.
6. Load-and-verify once the gateway runs; record the working install mechanism.
7. README section: install, env vars, what the badge means.
