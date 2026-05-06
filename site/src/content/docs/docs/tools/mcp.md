---
title: MCP connectors
description: Expose every tool of an MCP server to the agent loop in one go.
sidebar:
  order: 5
---

A [Model Context Protocol](https://modelcontextprotocol.io/) connector exposes every tool published by an MCP server to the agent loop in one go — no need to re-declare each tool individually. Like A2A, the protocol distinguishes by **where the server lives**:

| `kind`        | Resolved by | When to use it |
| ------------- | ----------- | -------------- |
| `mcp`         | server      | Streamable HTTP MCP server MANTYX can dial directly (a hosted GitHub MCP, a corporate Postgres MCP, …). MANTYX speaks `Initialize` + `tools/list` at run start and proxies every call. |
| `mcp_local`   | client      | MCP server that only your process can talk to (stdio, on-device, intranet). You point the SDK at the server (URL or stdio command) and the SDK does discovery and dispatch — using the official MCP SDK for the language under the hood. MANTYX is purely a transport. |

Whichever flavour you pick, **tools surface to the model as `<server>_<tool>`** so multiple servers can coexist without colliding. For `mcp` MANTYX prefixes server-side; for `mcp_local` the SDK auto-prefixes when serializing the catalog.

## Remote MCP — `mantyxMcp` / `MantyxMcp` / `mantyx_mcp`

```ts
import { mantyxMcp } from "@mantyx/sdk";

await client.runAgent({
  systemPrompt: "You are a developer assistant with GitHub access.",
  prompt: "Summarise the latest 5 issues on octocat/hello-world.",
  tools: [
    mantyxMcp({
      name: "github",
      url: "https://mcp.github.com/v1",
      headers: { Authorization: `Bearer ${process.env.GH_PAT}` },
      toolFilter: ["search_issues", "get_repo"],
    }),
  ],
});
```

```python
from mantyx import mantyx_mcp

client.run_agent(
    system_prompt="You are a developer assistant with GitHub access.",
    prompt="Summarise the latest 5 issues on octocat/hello-world.",
    tools=[
        mantyx_mcp(
            name="github",
            url="https://mcp.github.com/v1",
            headers={"Authorization": f"Bearer {os.environ['GH_PAT']}"},
            tool_filter=["search_issues", "get_repo"],
        ),
    ],
)
```

```go
client.RunAgent(ctx, mantyx.RunSpec{
    SystemPrompt: "You are a developer assistant with GitHub access.",
    Prompt:       "Summarise the latest 5 issues on octocat/hello-world.",
    Tools: []mantyx.ToolRef{
        mantyx.MantyxMcp(mantyx.MantyxMcpOptions{
            Name:       "github",
            URL:        "https://mcp.github.com/v1",
            Headers:    map[string]string{"Authorization": "Bearer " + os.Getenv("GH_PAT")},
            ToolFilter: []string{"search_issues", "get_repo"},
        }),
    },
})
```

| Field         | Required | Notes |
| ------------- | -------- | ----- |
| `name`        | yes      | Server label. MANTYX prefixes every discovered tool name as `<name>_<tool>`. Must match `^[a-zA-Z0-9_]{1,64}$`. |
| `url`         | yes      | Streamable HTTP MCP endpoint. |
| `headers`     | no       | Flat string→string HTTP headers (e.g. `Authorization`). Each value capped at 8 KB. |
| `toolFilter`  | no       | Allowlist of MCP tool names (un-prefixed, as the server returns them). When set, tools not in the list are silently dropped. |

If the MCP server is unreachable when the run starts, MANTYX still exposes a single stub tool named `<server>_unavailable` so the model can report the failure to the user instead of silently going without the catalog.

## Local MCP — `defineLocalMcp` / `LocalMcp` / `define_local_mcp`

When the MCP server only runs in your process — a stdio child process, a desktop integration, an intranet-only HTTP MCP — declare it as `mcp_local`. **MANTYX does no MCP work for this kind.** The SDK owns the entire MCP relationship: it opens the transport, runs `Initialize` + `tools/list`, ships the resolved catalog inline so MANTYX can render the tools to the model, and forwards every `local_tool_call` event MANTYX emits to the live MCP session via `tools/call`. The transport closes when the run / session ends.

The SDK API is **transport-only**: pass either a Streamable HTTP `url` (+ optional `headers`) or an stdio `command` (+ `args`/`env`/`cwd`). Internally each SDK uses the official MCP client for the language — `@modelcontextprotocol/sdk` for TypeScript, `mcp` for Python, `github.com/modelcontextprotocol/go-sdk` for Go — so you never construct an MCP client yourself.

### Streamable HTTP

```ts
import { defineLocalMcp } from "@mantyx/sdk";

defineLocalMcp({
  name: "fs",
  url: "http://localhost:8080/mcp",
  headers: { Authorization: `Bearer ${process.env.FS_TOKEN}` },
});
```

```python
from mantyx import define_local_mcp

define_local_mcp(
    name="fs",
    url="http://localhost:8080/mcp",
    headers={"Authorization": f"Bearer {os.environ['FS_TOKEN']}"},
)
```

```go
mantyx.LocalMcp(mantyx.LocalMcpSpec{
    Name:    "fs",
    URL:     "http://localhost:8080/mcp",
    Headers: map[string]string{"Authorization": "Bearer " + os.Getenv("FS_TOKEN")},
})
```

### stdio

```ts
defineLocalMcp({
  name: "fs",
  command: "mcp-server-filesystem",
  args: ["."],
  env: { FOO: "bar" },     // optional
  cwd: "/workspace",       // optional
});
```

```python
define_local_mcp(
    name="fs",
    command="mcp-server-filesystem",
    args=["."],
    env={"FOO": "bar"},     # optional
    cwd="/workspace",       # optional
)
```

```go
mantyx.LocalMcp(mantyx.LocalMcpSpec{
    Name:    "fs",
    Command: "mcp-server-filesystem",
    Args:    []string{"."},
    Env:     map[string]string{"FOO": "bar"}, // optional
    Cwd:     "/workspace",                    // optional
})
```

### Auto-prefixing tool names

The SDK auto-prefixes every tool's `name` with the server's `name` when serializing the catalog onto the wire and again when dispatching `local_tool_call` events into `tools/call`, so the model sees the same `<server>_<tool>` shape it would see for a remote `mantyxMcp`. The prefix is **idempotent** — if the upstream tool is already named `fs_read_file` and the server's `name` is `fs`, the SDK does not double-prefix.

| Field         | Required | Notes |
| ------------- | -------- | ----- |
| `name`        | yes      | SDK-side server label (e.g. `"fs"`, `"jira"`). Echoed back unchanged as `mcpServer` on every `local_tool_call`, **and** used to auto-prefix every tool's `name` on the wire. Must match `^[a-zA-Z0-9_]{1,64}$`. |
| `url`         | one-of   | Streamable HTTP MCP endpoint. Mutually exclusive with `command`. |
| `headers`     | no       | Forwarded **as-is** on every Streamable HTTP request to the upstream. Ignored for stdio. |
| `command`     | one-of   | Path to the binary (or shell-resolvable name) to launch over stdio. Mutually exclusive with `url`. |
| `args`        | no       | Command-line arguments passed to `command`. |
| `env`         | no       | Extra env vars merged on top of the parent process's environment. |
| `cwd`         | no       | Working directory for the child process. Empty inherits. |

### Per-run lifecycle

1. **Discovery (SDK, on the first run).** The SDK opens the transport — Streamable HTTP or stdio — and runs `Initialize` (capturing `serverInfo`) and `tools/list` (capturing the catalog). The session is kept open for the duration of the run / Mantyx session.
2. **Submission (SDK → MANTYX).** SDK posts the spec with the auto-prefixed catalog and `serverInfo`. MANTYX records the catalog snapshot on the run row and converts each `inputSchema` to a Zod-equivalent for tool-call validation.
3. **Tool call (MANTYX → SDK).** When the model calls a tool, MANTYX emits a `local_tool_call` event with `kind: "mcp_local"` and these extra hints so the SDK can dispatch to the right MCP client without re-parsing the tool name:

   ```jsonc
   {
     "seq": 9,
     "type": "local_tool_call",
     "data": {
       "toolUseId": "tu_x",
       "name": "fs_read_file",       // wire-prefixed name
       "args": { "path": "/etc/hosts" },
       "kind": "mcp_local",
       "mcpServer": "fs",            // ref's `name`
       "mcpToolName": "fs_read_file", // duplicates `name`
       "mcpServerInfo": {            // present iff the spec carried `serverInfo`
         "name": "mcp-server-filesystem",
         "version": "0.4.1"
       }
     }
   }
   ```

4. **Execution (SDK).** SDK looks up the live MCP session for `mcpServer`, strips the `<server>_` prefix from `mcpToolName` to recover the upstream tool name, calls `tools/call`, flattens the result content blocks to text, and POSTs the textual result back to `.../tool-results`.
5. **Cleanup.** When the run / session ends (or `Session.End()` is called), the SDK closes the MCP session and tears the transport down.

### Sanitizing upstream tool names

The MCP spec is permissive about tool naming, but MANTYX's wire protocol caps tool names at `^[a-zA-Z0-9_]{1,64}$`. The SDKs sanitize names that contain forbidden characters before shipping them; if your upstream uses `/` or `:` in tool names you may need to wrap or rename them upstream. The SDK's auto-prefixing turns sanitized names like `read_file` into `fs_read_file` on the wire.

End-to-end examples live at [`examples/mcp-tools`](/docs/examples/) for each SDK. The complete protocol contract — including the exact wire shape of the catalog, `serverInfo`, and `local_tool_call.mcpServerInfo` — is documented in the [wire protocol reference](https://github.com/mantyx-ai/mantyx-sdk/blob/main/docs/wire-protocol.md).
