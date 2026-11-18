// Pure status model: no OpenCode, no network, no timers. Unit-tested with node --test.
export type State = "up" | "idle" | "down" | "unknown"
export type Tone = "ok" | "warn" | "error" | "muted"

export interface Upstream {
  name: string
  transport: string
  state: State
  tools: number
  latency_ms: number | null
  last_ok: string | null
  last_error: string | null
  retry_in_s: number
}

export interface Snapshot {
  generated_at: string
  servers: Upstream[]
}

export interface Row {
  name: string
  glyph: string
  detail: string
  tone: Tone
}

export interface Summary {
  healthy: number // up + idle: idle means "worked before, closed by the idle timer"
  total: number
  badge: string
  tone: Tone
}

const STATES: readonly State[] = ["up", "idle", "down", "unknown"]

function isObject(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v)
}

function str(v: unknown, fallback: string): string {
  return typeof v === "string" ? v : fallback
}

function num(v: unknown, fallback: number): number {
  return typeof v === "number" && Number.isFinite(v) ? v : fallback
}

export function parseStatus(body: unknown): Snapshot {
  if (!isObject(body)) throw new Error("status: expected a JSON object")
  const servers = body.servers
  if (!Array.isArray(servers)) throw new Error("status: servers must be an array")
  return {
    generated_at: str(body.generated_at, ""),
    servers: servers.map((raw, i) => parseUpstream(raw, i)),
  }
}

function parseUpstream(raw: unknown, index: number): Upstream {
  if (!isObject(raw)) throw new Error(`status: servers[${index}] must be an object`)
  const name = str(raw.name, "")
  if (name === "") throw new Error(`status: servers[${index}].name is required`)

  const state = str(raw.state, "unknown")
  return {
    name,
    transport: str(raw.transport, "http"),
    // Unknown future states degrade to "unknown" instead of breaking the plugin.
    state: (STATES as readonly string[]).includes(state) ? (state as State) : "unknown",
    tools: num(raw.tools, 0),
    latency_ms: raw.latency_ms == null ? null : num(raw.latency_ms, 0),
    last_ok: raw.last_ok == null ? null : str(raw.last_ok, ""),
    last_error: raw.last_error == null ? null : str(raw.last_error, ""),
    retry_in_s: num(raw.retry_in_s, 0),
  }
}

export function summarize(s: Snapshot): Summary {
  const total = s.servers.length
  const healthy = s.servers.filter((u) => u.state === "up" || u.state === "idle").length
  const down = s.servers.filter((u) => u.state === "down").length

  if (total === 0) return { healthy: 0, total: 0, badge: "GW -", tone: "muted" }

  const badge = total > 99 ? `GW ${healthy}/99+` : `GW ${healthy}/${total}`
  let tone: Tone = "ok"
  if (down > 0) tone = healthy === 0 ? "error" : "warn"
  return { healthy, total, badge, tone }
}

// badgeFor is what the footer renders: config and load failures outrank data.
export function badgeFor(s: Snapshot | null, failures: number, hasToken: boolean): { badge: string; tone: Tone } {
  if (!hasToken) return { badge: "GW !", tone: "error" }
  if (failures >= 3) return { badge: "GW ?", tone: "muted" }
  if (s === null) return { badge: "GW …", tone: "muted" }
  const sum = summarize(s)
  return { badge: sum.badge, tone: sum.tone }
}

export function glyph(state: State): string {
  switch (state) {
    case "up":
      return "●"
    case "idle":
      return "◌"
    case "down":
      return "✗"
    default:
      return "?"
  }
}

export function toneOf(state: State): Tone {
  switch (state) {
    case "up":
      return "ok"
    case "down":
      return "error"
    case "idle":
      return "muted"
    default:
      return "muted"
  }
}

export function rows(s: Snapshot, limit = 8): Row[] {
  const shown = s.servers.slice(0, limit)
  const out: Row[] = shown.map((u) => ({
    name: truncate(u.name, 14),
    glyph: glyph(u.state),
    detail: detail(u),
    tone: toneOf(u.state),
  }))
  const rest = s.servers.length - shown.length
  if (rest > 0) out.push({ name: `+${rest} more`, glyph: "", detail: "", tone: "muted" })
  return out
}

function detail(u: Upstream): string {
  switch (u.state) {
    case "up":
      return `${u.tools}t ${u.latency_ms ?? "-"}ms`
    case "idle":
      return `${u.tools}t idle`
    case "down":
      return u.retry_in_s > 0 ? `down (retry ${u.retry_in_s}s)` : "down"
    default:
      return "-"
  }
}

export function truncate(s: string, n: number): string {
  const chars = Array.from(s)
  return chars.length <= n ? s : chars.slice(0, n).join("") + "…"
}

// describeError keeps UI/dialog errors short and strips anything token-shaped.
export function describeError(err: unknown, max = 120): string {
  let s = err instanceof Error ? err.message : typeof err === "string" ? err : "unknown error"
  s = s
    .replace(/\b(bearer)\s+\S+/gi, "$1 ***")
    .replace(/\b(token|api_key|apikey|password|secret)=[^\s&]+/gi, "$1=***")
  return truncate(s, max)
}
