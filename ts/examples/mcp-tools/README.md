# mcp-tools

Combine a **remote** MCP server (`mantyxMcp` — MANTYX dials it and proxies
each call) with a **local** MCP server (`defineLocalMcp` — the SDK declares
the catalog and dispatches each call into your process).

```bash
export MANTYX_API_KEY="mk_..."
export MANTYX_WORKSPACE_SLUG="acme-corp"
# Optional remote MCP server (omit to run with the local fs server only):
# export GH_MCP_URL="https://mcp.github.com/v1"
# export GH_PAT="ghp_..."
# export GH_TOOL_FILTER="search_issues,get_repo"

npm install
npm start "Show me the first 10 entries of the current working directory."
```

The example always exposes a `fs` MCP server (`fs_read_file`, `fs_list_dir`)
that runs in-process. Set `GH_MCP_URL` to also expose a GitHub MCP server;
`toolFilter` is supported via `GH_TOOL_FILTER`.

The example depends on the SDK via a local path (`"@mantyx/sdk": "file:../.."`).
If you copy this directory out of the monorepo, replace that with the
published version before running `npm install`.
