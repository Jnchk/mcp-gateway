import assert from "node:assert/strict"
import { test } from "node:test"

import { createPoller } from "./poller.ts"
import type { Snapshot } from "./status.ts"

const payload = {
  generated_at: "2026-11-17T11:20:03Z",
  servers: [{ name: "redmine", state: "up", tools: 9, latency_ms: 12 }],
}

const ok = { ok: true, status: 200, json: async () => payload } as unknown as Response
const fail = { ok: false, status: 503, json: async () => ({}) } as unknown as Response

const wait = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms))

function collector() {
  const snapshots: Snapshot[] = []
  const failures: Array<{ message: string; count: number }> = []
  return {
    snapshots,
    failures,
    opts: {
      url: "http://127.0.0.1:8930",
      token: "s3cret",
      intervalMs: 5000,
      onSnapshot: (s: Snapshot) => snapshots.push(s),
      onFailure: (message: string, count: number) => failures.push({ message, count }),
    },
  }
}

test("successful poll sends the bearer token and emits a snapshot", async () => {
  const c = collector()
  let seenURL = ""
  let seenAuth = ""
  const poller = createPoller({
    ...c.opts,
    fetchImpl: (async (url: string, init: RequestInit) => {
      seenURL = url
      seenAuth = (init.headers as Record<string, string>).Authorization
      return ok
    }) as unknown as typeof fetch,
  })
  await poller.refresh()
  poller.stop()

  assert.equal(seenURL, "http://127.0.0.1:8930/status")
  assert.equal(seenAuth, "Bearer s3cret")
  assert.equal(c.snapshots.length, 1)
  assert.equal(c.snapshots[0].servers[0].name, "redmine")
  assert.equal(c.failures.length, 0)
  assert.equal(poller.failures(), 0)
})

test("omits the header when no token is configured", async () => {
  const c = collector()
  let auth: string | undefined
  const poller = createPoller({
    ...c.opts,
    token: "",
    fetchImpl: (async (_url: string, init: RequestInit) => {
      auth = (init.headers as Record<string, string>).Authorization
      return ok
    }) as unknown as typeof fetch,
  })
  await poller.refresh()
  poller.stop()
  assert.equal(auth, undefined)
})

test("non-2xx responses count as failures", async () => {
  const c = collector()
  const poller = createPoller({ ...c.opts, fetchImpl: (async () => fail) as unknown as typeof fetch })
  await poller.refresh()
  poller.stop()

  assert.equal(c.snapshots.length, 0)
  assert.equal(c.failures.length, 1)
  assert.match(c.failures[0].message, /status endpoint returned 503/)
  assert.equal(poller.failures(), 1)
})

test("malformed payloads are reported, not emitted", async () => {
  const c = collector()
  const poller = createPoller({
    ...c.opts,
    fetchImpl: (async () => ({ ok: true, status: 200, json: async () => ({ nope: true }) })) as unknown as typeof fetch,
  })
  await poller.refresh()
  poller.stop()
  assert.equal(c.snapshots.length, 0)
  assert.match(c.failures[0].message, /servers must be an array/)
})

test("failures reset after a success", async () => {
  const c = collector()
  let call = 0
  const poller = createPoller({
    ...c.opts,
    fetchImpl: (async () => {
      call++
      return call === 1 ? fail : ok
    }) as unknown as typeof fetch,
  })
  await poller.refresh()
  assert.equal(poller.failures(), 1)
  await poller.refresh()
  poller.stop()
  assert.equal(poller.failures(), 0)
  assert.equal(c.snapshots.length, 1)
})

test("stop halts scheduled polls", async () => {
  const c = collector()
  let calls = 0
  const poller = createPoller({
    ...c.opts,
    intervalMs: 5,
    fetchImpl: (async () => {
      calls++
      return ok
    }) as unknown as typeof fetch,
  })
  poller.start()
  await wait(30)
  poller.stop()
  const after = calls
  await wait(40)
  assert.ok(after >= 2, `expected repeated polls, got ${after}`)
  assert.equal(calls, after)
})
