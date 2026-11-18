import assert from "node:assert/strict"
import { test } from "node:test"

import { DEFAULT_URL, clampInterval, nextDelay, normalizeURL, resolveConfig, statusURL } from "./config.ts"

test("defaults when nothing is configured", () => {
  assert.deepEqual(resolveConfig({}), { url: DEFAULT_URL, token: "", intervalMs: 5000 })
})

test("plugin options win over env and storage", () => {
  const got = resolveConfig(
    { MCP_GATEWAY_STATUS_URL: "http://env:1", MCP_GATEWAY_STATUS_TOKEN: "from-env", MCP_GATEWAY_STATUS_INTERVAL: "7000" },
    "http://stored:1",
    { url: "http://options:2", token: "from-options", intervalMs: 2500 },
  )
  assert.deepEqual(got, { url: "http://options:2", token: "from-options", intervalMs: 2500 })
})

test("optional option fields fall back to env", () => {
  const got = resolveConfig({ MCP_GATEWAY_STATUS_TOKEN: "from-env" }, "http://stored:1", { url: "http://options:2" })
  assert.deepEqual(got, { url: "http://options:2", token: "from-env", intervalMs: 5000 })
})

test("env wins over the stored URL", () => {
  const got = resolveConfig(
    { MCP_GATEWAY_STATUS_URL: "http://10.0.0.5:9000/", MCP_GATEWAY_STATUS_TOKEN: "  t  ", MCP_GATEWAY_STATUS_INTERVAL: "9000" },
    "http://stored:1",
  )
  assert.equal(got.url, "http://10.0.0.5:9000")
  assert.equal(got.token, "t")
  assert.equal(got.intervalMs, 9000)
})

test("stored URL is used when env is empty", () => {
  assert.equal(resolveConfig({}, "http://stored:1/").url, "http://stored:1")
})

test("normalizeURL validates", () => {
  assert.equal(normalizeURL("http://h:1/"), "http://h:1")
  assert.throws(() => normalizeURL("not a url"), /invalid MCP_GATEWAY_STATUS_URL/)
  assert.throws(() => normalizeURL("ftp://h"), /must be http/)
  assert.throws(() => normalizeURL("http://h/?a=1"), /query or fragment/)
  assert.throws(() => normalizeURL("http://h/#x"), /query or fragment/)
})

test("statusURL appends the path", () => {
  assert.equal(statusURL("http://127.0.0.1:8930"), "http://127.0.0.1:8930/status")
})

test("interval is clamped and validated", () => {
  assert.equal(clampInterval(10), 1000)
  assert.equal(clampInterval(999_999), 60_000)
  assert.equal(clampInterval(Number.NaN), 5000)
  assert.equal(clampInterval(2500.6), 2501)
})

test("backoff doubles and caps", () => {
  assert.equal(nextDelay(5000, 0), 5000)
  assert.equal(nextDelay(5000, 1), 10_000)
  assert.equal(nextDelay(5000, 2), 20_000)
  assert.equal(nextDelay(5000, 4), 60_000)
  assert.equal(nextDelay(5000, 9), 60_000)
})
