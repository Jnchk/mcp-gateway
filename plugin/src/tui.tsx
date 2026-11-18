// OpenCode TUI plugin: footer badge, sidebar list and a detail dialog.
// It only reads GET /status; it never calls MCP tools.
import { Plugin } from "@opencode/plugin/tui"
import { For, Show, createSignal } from "solid-js"

import { resolveConfig } from "./config.ts"
import { createPoller } from "./poller.ts"
import { badgeFor, describeError, glyph, rows, themeColor, toneOf, type Row, type Snapshot, type Tone } from "./status.ts"

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
      cfg = resolveConfig(env(), stored.url, context.options as { url?: string; token?: string; intervalMs?: number })
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

    function themeText(): Record<string, string> {
      const theme = context.theme as unknown as { text?: Record<string, string> }
      return theme.text ?? {}
    }

    function toneColor(tone: Tone): string {
      return themeColor(tone, themeText())
    }

    // Up rows read like a directly connected server (base color); every other
    // state uses the muted "connected label" grey.
    function baseColor(): string {
      return themeText().base ?? "white"
    }

    function rowTextColor(row: Row): string {
      return row.tone === "ok" ? baseColor() : themeColor("muted", themeText())
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

    // home.footer.status only renders on the home screen; prompt.footer.status
    // is the one visible while a session tab is open.
    const badgeView = () => <text fg={baseColor()}>{badge().badge}</text>
    const offFooter = context.ui.slot({ append: "home.footer.status", render: badgeView })
    const offPromptFooter = context.ui.slot({ append: "prompt.footer.status", render: badgeView })

    const offSidebar = context.ui.slot({
      append: "sidebar.content",
      render: () => (
        <box flexDirection="column">
          <For each={sidebarRows()}>
            {(row) => (
              <box flexDirection="row">
                <text fg={toneColor(row.tone)}>{row.glyph === "" ? "" : `${row.glyph} `}</text>
                <text fg={rowTextColor(row)}>{row.name}</text>
                <box flexGrow={1} />
                <Show when={row.detail !== ""}>
                  <text fg={rowTextColor(row)}>{row.detail}</text>
                </Show>
              </box>
            )}
          </For>
          <Show when={snap() === null}>
            <text fg={baseColor()}>
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
      offPromptFooter()
      offSidebar()
    }
  },
})
