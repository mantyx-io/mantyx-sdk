# mcp-tools

Combine a **remote** MCP server (`mantyx.MantyxMcp` — MANTYX dials it and
proxies each call) with a **local** MCP server (`mantyx.LocalMcp` — the SDK
opens the transport, runs `Initialize` + `tools/list` on the first run, and
forwards `tools/call` for every `local_tool_call` event MANTYX emits for
this server, when the platform can't reach the upstream).

Tools are surfaced to the model as `<server>_<tool>` regardless of whether
they live remotely or locally — see `docs/agent-runs-protocol.md` §4.3.

```bash
export MANTYX_API_KEY="mk_..."
export MANTYX_WORKSPACE_SLUG="acme-corp"

# Pick one local transport:
export FS_MCP_URL="http://localhost:8080/mcp"      # Streamable HTTP
# OR
export FS_MCP_COMMAND="mcp-server-filesystem ."    # stdio (space-split)

# Optional — register a real public MCP server too:
# export GH_MCP_URL="https://mcp.github.com/v1"
# export GH_PAT="ghp_..."
# export GH_TOOL_FILTER="search_issues,get_repo"

go run . "List the first 10 entries of the current working directory."
```

`mantyx.LocalMcp` is **URL-only** for HTTP and **command-only** for stdio.
The SDK uses the official `github.com/modelcontextprotocol/go-sdk/mcp`
package internally to open the transport, discover the tool catalog, ship
it inline so MANTYX can render the tools to the model, run `tools/call`
against the live session for every `local_tool_call`, and close the
transport when the run / session ends (`session.End()` for sessions,
automatically for one-shot runs).

This module's `go.mod` has a `replace` directive pointing back at `../..`
so it builds against the in-tree SDK. When you copy it out of the repo,
remove that `replace` and run `go mod tidy`.
