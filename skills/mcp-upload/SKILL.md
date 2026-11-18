---
name: mcp-upload
description: Stage a local file through mcp-gateway and return the id a tool can consume. Use for any upstream that accepts upload ids (Redmine attachments, Obsidian note bodies, and any other server implementing the staging contract).
---

# mcp-upload

Neutral client for the gateway's staging route. It knows nothing about Redmine or Obsidian: `--server`
is whichever upstream name is configured in `~/.config/mcp-gateway/config.json`.

```
POST http://<gateway>/upload/<server>?filename=<name>
Authorization: Bearer <gateway token>
Content-Type: application/octet-stream
→ 201 {"id": "...", "filename": "...", "size": 123, "sha256": "..."}
```

The id is single-use and expires (the upstream decides the TTL; 15 minutes is the common default).
Pass it to the tool that consumes uploads, for example:

```js
// Redmine
await tools.gateway.call_tool({ server: "redmine", tool: "create_issue",
  arguments: { project: "...", subject: "...", attach_upload_ids: ["<id>"] } })

// Obsidian note body
await tools.gateway.call_tool({ server: "obsitrace", tool: "write",
  arguments: { path: "Inbox/report.md", contentUploadId: "<id>" } })
```

## Usage

```bash
python3 ~/.config/opencode/skills/mcp-upload/scripts/mcp_upload.py --server <upstream> --path FILE
```

In this repository the script is at `skills/mcp-upload/scripts/mcp_upload.py`.

Flags: `--server` (required), `--path` (required), `--filename`, `--url`, `--token`, `--config`,
`--timeout`, `--json` (print the whole response). The token and gateway address come from
`~/.config/mcp-gateway/config.json` (or `$MCP_GATEWAY_CONFIG`); `--url`/`--token`/`MCP_GATEWAY_URL`/
`MCP_GATEWAY_TOKEN` override them. Never pass `--token` on the command line: it lands in shell history.

## Requirements

- The gateway must have that upstream configured, and its `upload_requires_any` tools must be allowed
  (otherwise the gateway answers 403 without contacting the upstream).
- The upstream must implement `POST /upload` and return a JSON body with an `id`. Servers that only
  expose MCP tools have no staging route and will fail with the upstream's 404.
- The file must be non-empty, and text-only servers reject invalid UTF-8.
- Staging routes are HTTP-only; stdio upstreams need the tool's inline content instead.

Exit codes: `0` printed an id · `2` usage/config · `3` HTTP error · `4` local file error.
