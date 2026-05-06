---
title: Local tools
description: Tools whose handler runs inside your process and shuttles results to MANTYX.
sidebar:
  order: 1
---

A local tool is **defined and executed in the SDK's process**. When the model calls it, MANTYX pauses the agent loop, emits a `local_tool_call` event over SSE, and waits for the SDK to POST a tool-result back via HTTP.

This is how you give the agent access to anything that requires running code in your environment — the local filesystem, an internal HTTP service, a native library, secrets that can't leave your machine.

## Three flavours of "local"

The wire protocol exposes three client-resolved tool kinds. They share a transport — `local_tool_call` event in, tool-result POST out — but differ by which SDK-side helper builds them and which `kind` discriminator MANTYX echoes on the event:

| `kind`        | Helper                                                | Use it when |
| ------------- | ----------------------------------------------------- | ----------- |
| `local`       | `defineLocalTool` / `LocalTool` / `define_local_tool` | Generic in-process function — filesystem, native library, internal HTTP. |
| `a2a_local`   | `defineLocalA2A` / `LocalA2A` / `define_local_a2a`    | An [A2A](/docs/tools/a2a/) peer only your process can reach. |
| `mcp_local`   | `defineLocalMcp` / `LocalMcp` / `define_local_mcp`    | A whole [MCP](/docs/tools/mcp/) server only your process can reach. |

This page covers `kind: "local"`. The two specialised helpers are documented on their own pages.

## Defining a local tool

```ts
import { defineLocalTool } from "@mantyx/sdk";
import { z } from "zod";

const tool = defineLocalTool({
  name: "read_file",
  description: "Read a UTF-8 file from the local filesystem.",
  parameters: z.object({ path: z.string() }),
  execute: async ({ path }) => {
    const fs = await import("node:fs/promises");
    return fs.readFile(path, "utf8");
  },
});
```

```python
from pydantic import BaseModel
from mantyx import define_local_tool

class ReadFileArgs(BaseModel):
    path: str

tool = define_local_tool(
    name="read_file",
    description="Read a UTF-8 file from the local filesystem.",
    parameters=ReadFileArgs,
    execute=lambda args: open(args.path).read(),
)
```

```go
type readFileArgs struct {
    Path string `json:"path" jsonschema:"description=Path to the file to read"`
}

tool := mantyx.LocalTool(mantyx.LocalToolSpec{
    Name:        "read_file",
    Description: "Read a UTF-8 file from the local filesystem.",
    Parameters:  &readFileArgs{},
    Execute: func(ctx context.Context, raw json.RawMessage) (string, error) {
        var args readFileArgs
        if err := json.Unmarshal(raw, &args); err != nil { return "", err }
        b, err := os.ReadFile(args.Path)
        return string(b), err
    },
})
```

## Naming rules

The tool name must match `^[a-zA-Z0-9_]{1,64}$`. The SDK validates this client-side; the server enforces it as well.

## Parameter schemas

The SDK converts your local schema definition (Zod / Pydantic / tagged Go struct) into a JSON Schema that the server feeds to LLM providers. Unsupported features (effects, transforms, intersections) degrade to a permissive `"object"` rather than failing the request.

For best results, keep schemas to the JSON-Schema-friendly intersection: `string`, `number`, `boolean`, `array`, nested `object`, plus optional / nullable / default. Add a `description` to each field — the model uses it to decide when to call the tool.

## Returning a result

The handler must return a **string**. For structured outputs, JSON-serialize before returning:

```ts
execute: async () => JSON.stringify({ ok: true, count: 42 });
```

A thrown error (or a non-`nil` `error` in Go) is forwarded to the model as a tool-error response. You typically don't need to catch and re-throw; the SDK wraps the message into the right wire shape automatically.

## Timeouts

The server enforces a tool-result timeout (default 60s) for each `local_tool_call`. If the SDK doesn't POST a result in time, the run terminates with `result.subtype = "error_local_tool_timeout"`.

To run long-running work, persist the result somewhere durable and have the tool body return a "queued" message; on a follow-up turn, return the actual result via a different tool that reads from the durable store.

## How dispatch works

Each SDK keeps three small registries keyed by tool name — one for generic local handlers, one for [local A2A](/docs/tools/a2a/) peers, one for [local MCP](/docs/tools/mcp/) servers. On a `local_tool_call` SSE event the SDK switches on the `kind` field in the payload:

- `kind` omitted or `"local"` → look up `name` in the generic registry, validate `args` against the schema, run the handler.
- `kind: "a2a_local"` → look up `name` in the A2A registry, take the cached Agent Card resolved from the supplied `agentCardUrl`, dispatch the `args.message` over JSON-RPC `message/send`, and post the reply text back.
- `kind: "mcp_local"` → look up the server in the MCP registry by `mcpServer`, take the live MCP session opened from the supplied `url` / `command`, strip the `<server>_` prefix from `mcpToolName`, dispatch via `tools/call`, and post the flattened text content back.

You don't normally see this dispatch in user code — `runAgent` / `streamAgent` / `session.send` does it for you. It's only relevant when you're implementing a third-party SDK against the [Wire protocol](/docs/protocol/).
