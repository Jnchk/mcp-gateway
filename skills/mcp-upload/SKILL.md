---
name: mcp-upload
description: Stage a local file through mcp-gateway's upload route and return the id a tool can consume. Use when a file on this machine must reach any upstream that accepts upload ids.
---

# mcp-upload

Neutral client for the gateway's staging route. It knows nothing about any particular upstream: `--server`
is whichever upstream name the gateway has configured.

```
POST http://<gateway>/upload/<server>?filename=<name>
Authorization: Bearer <gateway token>
Content-Type: application/octet-stream
→ 201 {"id": "...", "filename": "...", "size": 123, "sha256": "..."}
```

The id is single-use and expires (the upstream decides the TTL; 15 minutes is the common default).
Pass it to the argument the consuming tool documents for upload ids; the argument name differs per
upstream and is named by that upstream's own skill.

## Usage

```bash
# run from the root of your mcp-gateway clone
python3 skills/mcp-upload/scripts/mcp_upload.py --server <upstream> --path FILE
```

Flags: `--server` (required), `--path` (required), `--filename`, `--url`, `--token`, `--config`,
`--timeout`, `--json` (print the whole response).

## Client config

The client reads its own file, `~/.config/mcp-gateway/client.json` (or `$MCP_GATEWAY_CONFIG`), mode 600:

```json
{
  "url": "http://192.168.101.91:8930",
  "token": "<gateway token>"
}
```

That is deliberately not the gateway's `config.json`, which also lists the upstream servers and their
credentials. `url` may instead be spelled `listen` (the gateway's own key) for an already-working local
setup. An optional `servers` object restricts the `--server` names this client may send; without it the
gateway is the only judge. `--url`/`--token`/`MCP_GATEWAY_URL`/`MCP_GATEWAY_TOKEN` override the file.
Never pass `--token` on the command line: it lands in shell history.

## Requirements

- The gateway must have that upstream configured, and its `upload_requires_any` tools must be allowed
  (otherwise the gateway answers 403 without contacting the upstream).
- The upstream must implement `POST /upload` and return a JSON body with an `id`. Servers that only
  expose MCP tools have no staging route and will fail with the upstream's 404.
- The file must be non-empty, and text-only servers reject invalid UTF-8.
- Staging routes are HTTP-only; stdio upstreams need the tool's inline content instead.

Exit codes: `0` printed an id · `2` usage/config · `3` HTTP error · `4` local file error.
