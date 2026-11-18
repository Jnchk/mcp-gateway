// Configuration resolution for the plugin. Pure: env in, settings out.
export interface Settings {
  url: string
  token: string
  intervalMs: number
}

// Supplied from cli.json plugin options; the documented way to configure a
// local TUI plugin without environment variables.
export interface Options {
  url?: string
  token?: string
  intervalMs?: number
}

export const DEFAULT_URL = "http://127.0.0.1:8930"
export const DEFAULT_INTERVAL_MS = 5000

export function resolveConfig(
  env: Record<string, string | undefined>,
  storedURL?: string,
  options: Options = {},
): Settings {
  // Plugin options win: they live in cli.json, not in the environment.
  const url = normalizeURL(firstNonEmpty(options.url, env.MCP_GATEWAY_STATUS_URL, storedURL, DEFAULT_URL))
  const token = (firstNonEmpty(options.token, env.MCP_GATEWAY_STATUS_TOKEN)).trim()
  const rawInterval = options.intervalMs ?? env.MCP_GATEWAY_STATUS_INTERVAL ?? DEFAULT_INTERVAL_MS
  return { url, token, intervalMs: clampInterval(Number(rawInterval)) }
}

function firstNonEmpty(...values: Array<string | undefined>): string {
  for (const v of values) if (v !== undefined && v.trim() !== "") return v.trim()
  return ""
}

export function normalizeURL(input: string): string {
  let u: URL
  try {
    u = new URL(input)
  } catch {
    throw new Error(`invalid MCP_GATEWAY_STATUS_URL: ${input}`)
  }
  if (u.protocol !== "http:" && u.protocol !== "https:") {
    throw new Error(`status URL must be http(s), got ${u.protocol}`)
  }
  if (u.search !== "" || u.hash !== "") {
    throw new Error("status URL must not contain a query or fragment")
  }
  const path = u.pathname.replace(/\/+$/, "")
  return `${u.origin}${path}`
}

export function statusURL(base: string): string {
  return `${base}/status`
}

export function clampInterval(ms: number): number {
  if (!Number.isFinite(ms)) return DEFAULT_INTERVAL_MS
  return Math.min(60_000, Math.max(1_000, Math.round(ms)))
}

// Backoff grows with consecutive failures, capped at one minute.
export function nextDelay(baseMs: number, failures: number): number {
  if (failures <= 0) return clampInterval(baseMs)
  const grown = baseMs * Math.pow(2, Math.min(failures, 4))
  return clampInterval(grown)
}
