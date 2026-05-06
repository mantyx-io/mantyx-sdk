# mcp-tools

Combine a **remote** MCP server (`mantyx_mcp` — MANTYX dials and proxies
it) with a **local** MCP server (`define_local_mcp` — the SDK opens the
transport, runs `Initialize` + `tools/list` on the first run, and forwards
`tools/call` for every `local_tool_call` event MANTYX emits for this
server, when the platform can't reach the upstream).

Tools are surfaced to the model as `<server>_<tool>` regardless of whether
they live remotely or locally — see `docs/agent-runs-protocol.md` §4.3.

```bash
export MANTYX_API_KEY="mtx_live_..."
export MANTYX_WORKSPACE_SLUG="acme-corp"

# Pick one local transport:
export FS_MCP_URL="http://localhost:8080/mcp"      # Streamable HTTP
# OR
export FS_MCP_COMMAND="mcp-server-filesystem ."    # stdio (space-split)

# Optional — register a real public MCP server too:
# export GH_MCP_URL="https://api.example.com/mcp"
# export GH_PAT="..."
# export GH_TOOL_FILTER="create_issue,list_issues"

uv run python main.py "List the first 10 entries of the current directory."
```

`define_local_mcp` is **URL-only** for HTTP and **command-only** for
stdio. The SDK uses the official [`mcp`](https://pypi.org/project/mcp/)
package internally to open the transport, discover the tool catalog,
ship it inline so MANTYX can render the tools to the model, run
`tools/call` against the live session for every `local_tool_call`, and
close the transport when the run / session ends. Sync clients drive the
async MCP SDK transparently via an `anyio.BlockingPortal`.
