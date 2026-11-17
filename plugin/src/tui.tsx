// OpenCode TUI plugin: footer badge, sidebar list and a detail dialog.
// It only reads GET /status; it never calls MCP tools.
import { Plugin } from "@opencode/plugin/tui"
import { For, Show, createSignal } from "solid-js"

import { resolveConfig } from "./config.ts"
import { createPoller } from "./poller.ts"
import { badgeFor, describeError, glyph, rows, toneOf, type Row, type Snapshot, type Tone } from "./status.ts"

function env(): Record<string, string | undefined> {
  return typeof process === "undefined" ? {} : process.env
}

export default Plugin.define({
  id: "mcp.gateway.status",
  setup(context) {
    const [stored] = context.storage.store("gateway-status", { initial: { url: "" } })

    let cfg: { url: string; token: string; intervalMs: number }
    let configError = ""
    try {
      cfg = resolveConfig(env(), stored.url)
    } catch (err) {
      configError = describeError(err)
      cfg = { url: "", token: "", intervalMs: 5000 }
    }

    const [snap, setSnap] = createSignal<Snapshot | null>(null)
    const [failures, setFailures] = createSignal(0)
    const [lastAt, setLastAt] = createSignal<number | null>(null)
    const [errorText, setErrorText] = createSignal("")

    const poller = createPoller({
      url: cfg.url,
      token: cfg.token,
      intervalMs: cfg.intervalMs,
      onSnapshot: (s) => {
        setSnap(s)
        setFailures(0)
        setLastAt(Date.now())
        setErrorText("")
      },
      onFailure: (message, count) => {
        setFailures(count)
        setErrorText(message)
      },
    })
    if (cfg.token !== "" && configError === "") poller.start()

    function toneColor(tone: Tone): string {
      const theme = context.theme as unknown as { text?: Record<string, string> }
      const text = theme.text ?? {}
      const base = text.base ?? "white"
      switch (tone) {
        case "ok":
          return text.success ?? base
        case "warn":
          return text.warning ?? base
        case "error":
          return text.error ?? base
        default:
          return text.muted ?? base
      }
    }

    const badge = () => badgeFor(snap(), failures(), cfg.token !== "" && configError === "")

    const sidebarRows = (): Row[] => {
      const s = snap()
      return s === null ? [] : rows(s)
    }

    const lastLabel = () => {
      const at = lastAt()
      if (at === null) return "no data yet"
      const seconds = Math.max(0, Math.round((Date.now() - at) / 1000))
      return `updated ${seconds}s ago`
    }

    const offFooter = context.ui.slot({
      append: "home.footer.status",
      render: () => <text fg={toneColor(badge().tone)}>{badge().badge}</text>,
    })

    const offSidebar = context.ui.slot({
      append: "sidebar.content",
      render: () => (
        <box flexDirection="column">
          <For each={sidebarRows()}>
            {(row) => (
              <text fg={toneColor(row.tone)}>
                {row.glyph === "" ? row.name : `${row.glyph} ${row.name} ${row.detail}`}
              </text>
            )}
          </For>
          <Show when={snap() === null}>
            <text fg={toneColor("muted")}>
              {configError !== "" ? configError : cfg.token === "" ? "set MCP_GATEWAY_STATUS_TOKEN" : "loading gateway status"}
            </text>
          </Show>
        </box>
      ),
    })

    const Details = () => (
      <box flexDirection="column">
        <text fg={toneColor("muted")}>{`gateway ${cfg.url} · ${lastLabel()}`}</text>
        <For each={snap()?.servers ?? []}>
          {(u) => (
            <text fg={toneColor(toneOf(u.state))}>
              {`${glyph(u.state)} ${u.name.padEnd(18)} ${u.state.padEnd(8)} ${u.tools}t ${u.latency_ms ?? "-"}ms${
                u.last_error === null ? "" : "  " + describeError(u.last_error, 80)
              }`}
            </text>
          )}
        </For>
        <Show when={snap() === null && cfg.token !== ""}>
          <text fg={toneColor("muted")}>no snapshot yet</text>
        </Show>
        <Show when={errorText() !== ""}>
          <text fg={toneColor("error")}>{`last error: ${errorText()}`}</text>
        </Show>
        <Show when={configError !== ""}>
          <text fg={toneColor("error")}>{configError}</text>
        </Show>
        <Show when={cfg.token === "" && configError === ""}>
          <text fg={toneColor("error")}>export MCP_GATEWAY_STATUS_TOKEN to start polling</text>
        </Show>
        <text fg={toneColor("muted")}>esc closes · /gateway-status reopens</text>
      </box>
    )

    context.keymap.layer(() => ({
      mode: "global",
      commands: [
        {
          id: "mcp.gateway.status.show",
          title: "MCP gateway status",
          group: "MCP Gateway",
          palette: true,
          slash: { name: "gateway-status" },
          run: () => {
            context.ui.dialog.set({ size: "large", centered: true })
            context.ui.dialog.show(() => <Details />, () => {})
          },
        },
      ],
    }))

    return () => {
      poller.stop()
      offFooter()
      offSidebar()
    }
  },
})
