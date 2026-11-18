import assert from "node:assert/strict"
import { test } from "node:test"

import {
  badgeFor,
  describeError,
  glyph,
  parseStatus,
  rows,
  summarize,
  truncate,
  type Snapshot,
} from "./status.ts"

const payload = {
  generated_at: "2026-11-17T11:20:03Z",
  servers: [
    { name: "redmine", transport: "http", state: "up", tools: 9, latency_ms: 12, last_ok: "2026-11-17T11:19:58Z", last_error: null },
    { name: "reviewboard", transport: "http", state: "down", tools: 0, latency_ms: null, last_ok: null, last_error: "refused", retry_in_s: 3 },
    { name: "idle-one", transport: "stdio", state: "idle", tools: 2, latency_ms: null, last_ok: "2026-11-17T11:00:00Z", last_error: null },
  ],
}

test("parseStatus accepts the gateway document", () => {
  const s = parseStatus(payload)
  assert.equal(s.generated_at, "2026-11-17T11:20:03Z")
  assert.equal(s.servers.length, 3)
  assert.deepEqual(s.servers[1], {
    name: "reviewboard",
    transport: "http",
    state: "down",
    tools: 0,
    latency_ms: null,
    last_ok: null,
    last_error: "refused",
    retry_in_s: 3,
  })
})

test("parseStatus rejects malformed documents", () => {
  assert.throws(() => parseStatus(null), /expected a JSON object/)
  assert.throws(() => parseStatus([]), /expected a JSON object/)
  assert.throws(() => parseStatus({}), /servers must be an array/)
  assert.throws(() => parseStatus({ servers: [null] }), /servers\[0\] must be an object/)
  assert.throws(() => parseStatus({ servers: [{}] }), /name is required/)
})

test("parseStatus degrades unknown states and ignores extra fields", () => {
  const s = parseStatus({ servers: [{ name: "x", state: "exploded", extra: true }] })
  assert.equal(s.servers[0].state, "unknown")
  assert.equal(s.servers[0].tools, 0)
  assert.equal(s.servers[0].latency_ms, null)
})

test("summarize counts healthy upstreams and tones", () => {
  const allUp: Snapshot = { generated_at: "", servers: [{ ...parseStatus(payload).servers[0] }] }
  assert.deepEqual(summarize(allUp), { healthy: 1, total: 1, badge: "GW 1/1", tone: "ok" })

  // idle counts as healthy: it worked before and was closed by the idle timer.
  const allIdle = summarize(parseStatus({ servers: [payload.servers[2]] }))
  assert.deepEqual(allIdle, { healthy: 1, total: 1, badge: "GW 1/1", tone: "ok" })

  const mixed = summarize(parseStatus(payload))
  assert.equal(mixed.healthy, 2)
  assert.equal(mixed.total, 3)
  assert.equal(mixed.badge, "GW 2/3")
  assert.equal(mixed.tone, "warn")

  const allDown = summarize(parseStatus({ servers: [payload.servers[1]] }))
  assert.equal(allDown.tone, "error")

  assert.deepEqual(summarize({ generated_at: "", servers: [] }), { healthy: 0, total: 0, badge: "GW -", tone: "muted" })
})

test("badge stays short for large fleets", () => {
  const servers = Array.from({ length: 150 }, (_, i) => ({
    ...parseStatus(payload).servers[0],
    name: `s${i}`,
  }))
  const got = summarize({ generated_at: "", servers })
  assert.equal(got.badge, "GW 150/99+")
  assert.ok(got.badge.length <= 10)
})

test("badgeFor prioritises config and staleness over data", () => {
  const snap = parseStatus(payload)
  assert.deepEqual(badgeFor(null, 0, false), { badge: "GW !", tone: "error" })
  assert.deepEqual(badgeFor(null, 0, true), { badge: "GW …", tone: "muted" })
  assert.deepEqual(badgeFor(snap, 3, true), { badge: "GW ?", tone: "muted" })
  assert.deepEqual(badgeFor(snap, 2, true), { badge: "GW 2/3", tone: "warn" })
})

test("rows render at most the limit plus a remainder", () => {
  const snap = parseStatus({ servers: Array.from({ length: 11 }, (_, i) => ({ name: `server-${i}`, state: "up", tools: i })) })
  const got = rows(snap)
  assert.equal(got.length, 9)
  assert.equal(got[8].name, "+3 more")
  assert.equal(got[0].glyph, "●")
  assert.match(got[0].detail, /0t/)
})

test("rows truncate long names", () => {
  const snap = parseStatus({ servers: [{ name: "a-very-long-server-name", state: "up" }] })
  const [row] = rows(snap)
  assert.equal(row.name, "a-very-long-se…")
  assert.equal(Array.from(row.name).length, 15)
})

test("down rows mention the retry delay", () => {
  const snap = parseStatus(payload)
  const down = rows(snap).find((r) => r.name === "reviewboard")
  assert.equal(down?.detail, "down (retry 3s)")
  assert.equal(down?.tone, "error")
})

test("glyphs cover every state", () => {
  assert.equal(glyph("up"), "●")
  assert.equal(glyph("idle"), "◌")
  assert.equal(glyph("down"), "✗")
  assert.equal(glyph("unknown"), "?")
})

test("truncate is rune safe", () => {
  assert.equal(truncate("登录失败在单点登录", 4), "登录失败…")
  assert.equal(truncate("short", 10), "short")
})

test("describeError redacts credentials", () => {
  const msg = describeError(new Error("Authorization: Bearer abc123 rejected; token=deadbeef failed"))
  assert.ok(!msg.includes("abc123"))
  assert.ok(!msg.includes("deadbeef"))
  assert.match(msg, /Bearer \*\*\*/)
  assert.match(msg, /token=\*\*\*/)
})

test("describeError bounds the message", () => {
  const got = describeError(new Error("x".repeat(500)))
  assert.equal(Array.from(got).length, 121)
  assert.ok(got.endsWith("…"))
})
