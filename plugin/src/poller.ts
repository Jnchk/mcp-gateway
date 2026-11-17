// Polling loop with abort, single-flight and backoff. Pure enough to unit-test:
// fetch is injectable and the timers use standard globals.
import { nextDelay, statusURL } from "./config.ts"
import { describeError, parseStatus, type Snapshot } from "./status.ts"

export interface PollerOptions {
  url: string
  token: string
  intervalMs: number
  timeoutMs?: number
  fetchImpl?: typeof fetch
  onSnapshot(snapshot: Snapshot): void
  onFailure(message: string, failures: number): void
}

export interface Poller {
  start(): void
  stop(): void
  refresh(): Promise<void>
  failures(): number
}

export function createPoller(o: PollerOptions): Poller {
  const timeoutMs = o.timeoutMs ?? 2000
  const doFetch = o.fetchImpl ?? globalThis.fetch
  let timer: ReturnType<typeof setTimeout> | undefined
  let controller: AbortController | undefined
  let stopped = false
  let inflight = false
  let failures = 0

  function schedule(ms: number): void {
    if (stopped) return
    clearTimeout(timer)
    timer = setTimeout(() => {
      void tick()
    }, ms)
  }

  async function tick(): Promise<void> {
    if (stopped || inflight) return
    inflight = true
    controller = new AbortController()
    const timeout = setTimeout(() => controller?.abort(), timeoutMs)
    try {
      const headers: Record<string, string> = {}
      if (o.token !== "") headers.Authorization = `Bearer ${o.token}`
      const res = await doFetch(statusURL(o.url), { headers, signal: controller.signal })
      if (!res.ok) throw new Error(`status endpoint returned ${res.status}`)
      const snapshot = parseStatus(await res.json())
      failures = 0
      o.onSnapshot(snapshot)
      schedule(o.intervalMs)
    } catch (err) {
      failures++
      o.onFailure(describeError(err), failures)
      schedule(nextDelay(o.intervalMs, failures))
    } finally {
      clearTimeout(timeout)
      inflight = false
    }
  }

  return {
    start() {
      if (!stopped && !inflight) void tick()
    },
    stop() {
      stopped = true
      clearTimeout(timer)
      controller?.abort()
    },
    refresh() {
      if (stopped || inflight) return Promise.resolve()
      return tick()
    },
    failures() {
      return failures
    },
  }
}
