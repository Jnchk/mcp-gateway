#!/usr/bin/env bash
# Runs the live functional test against a redmine-mcp instance.
#
#   scripts/functional.sh [env-file]
#
# Defaults to ~/.config/redmine-mcp-neo/env, which must define MCP_ADDR and MCP_TOKEN.
set -euo pipefail

env_file="${1:-${REDMINE_MCP_ENV:-}}"
if [[ -z "$env_file" || ! -r "$env_file" ]]; then
  echo "cannot read ${env_file:-<unset>}" >&2
  echo "usage: $0 <env-file with MCP_ADDR and MCP_TOKEN>   (or set REDMINE_MCP_ENV)" >&2
  exit 1
fi

# shellcheck disable=SC1090
set -a
. "$env_file"
set +a

addr="${MCP_ADDR%%,*}"
addr="${addr// /}"
export REDMINE_MCP_URL="http://${addr}/mcp"
export REDMINE_MCP_TOKEN="${MCP_TOKEN}"

if [[ -z "${REDMINE_MCP_TOKEN}" ]]; then
  echo "MCP_TOKEN is empty in $env_file" >&2
  exit 1
fi

cd "$(dirname "$0")/.."
go test -tags functional -count=1 -v ./test
