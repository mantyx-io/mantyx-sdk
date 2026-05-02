# MANTYX SDKs

Official client SDKs for the [MANTYX](https://mantyx.com) agent runtime.

MANTYX is an agent operating system: it owns the LLM loop, the workspace tool
catalog, memory, skills, and persisted observability. The SDKs let you drive
that runtime from your own process — define ephemeral agents inline, trigger
persisted MANTYX agents by id, and seamlessly mix **remote** workspace tools
with **local** tools that run in your process and shuttle results back over
the agent loop.

```
packages/mantyx-sdk/
├── README.md         ← you are here
├── ts/               ← @mantyx/sdk           (npm, TypeScript / Node.js)
└── go/               ← github.com/mantyx/mantyx-go-sdk (Go module)
```

Both implementations target the same wire protocol and feature set; pick the
one that matches your stack.

| | TypeScript | Go |
| --- | --- | --- |
| Source | [`ts/`](./ts) | [`go/`](./go) |
| Package | `@mantyx/sdk` | `github.com/mantyx/mantyx-go-sdk` |
| Install | `npm install @mantyx/sdk zod` | `go get github.com/mantyx/mantyx-go-sdk` |
| Min runtime | Node.js 18.17+ | Go 1.22+ |
| Local tool params | Zod schema | tagged Go struct (via `invopop/jsonschema`) |
| Examples | [`ts/examples/`](./ts/examples) | [`go/examples/`](./go/examples) |

## What you can do with the SDKs

- **Run an ephemeral agent** — describe a system prompt, model, and tool list
  on the call site. MANTYX runs the loop and streams results back.
- **Trigger a persisted MANTYX agent (`agentId`)** — reuse an agent that
  already lives in your workspace (with its system prompt, model, memory,
  skills, and tool list) and optionally merge in extra `local` tools for
  that single run.
- **Maintain conversational sessions** — multi-turn agent runs whose history
  persists on the server, with optional per-turn tool refresh.
- **Mix remote and local tools** — `mantyx` (workspace `Tool`), `mantyx_plugin`
  (platform plugin tools), and `local` (executed in your process).
- **Stream tokens** — assistant deltas, thinking deltas, server tool results,
  local tool calls, and the terminal `result` event over SSE.
- **Pick a model** — choose a workspace BYOK provider, a specific vendor
  model, or a platform-hosted offering via a unified `modelId` string.
- **Tag for observability** — attach a flat `metadata` KV (e.g.
  `{ customer: "acme", env: "prod" }`) to runs and sessions so your team can
  filter the dashboard by them. See each SDK's README for the full snippet.

## Authentication

Every call requires a workspace API key with usage `developer_api`. Generate
one in **Settings → API keys** in the MANTYX dashboard. The key is scoped to
a workspace slug; the SDKs send it as `Authorization: Bearer <key>`.

## Wire protocol

Both SDKs talk the same HTTP + SSE protocol. The full specification lives in
[`docs/agent-runs-protocol.md`](./docs/agent-runs-protocol.md) (a copy is
also kept under each SDK's `docs/` for the standalone OSS extracts).

If you want to write a third-party SDK or call the surface directly with
`curl`, the protocol doc is the source of truth.

## Quickstart

### TypeScript

```ts
import { MantyxClient, defineLocalTool } from "@mantyx/sdk";
import { z } from "zod";
import { readFile } from "node:fs/promises";

const client = new MantyxClient({
  apiKey: process.env.MANTYX_API_KEY!,
  workspaceSlug: "acme-corp",
});

const result = await client.runAgent({
  systemPrompt: "You are a helpful filesystem assistant.",
  prompt: "Read /etc/hostname and tell me what it says.",
  tools: [
    defineLocalTool({
      name: "read_file",
      parameters: z.object({ path: z.string() }),
      execute: ({ path }) => readFile(path, "utf8"),
    }),
  ],
});
console.log(result.text);
```

See [`ts/README.md`](./ts/README.md) for the full reference.

### Go

```go
package main

import (
    "context"
    "encoding/json"
    "fmt"
    "log"
    "os"

    mantyx "github.com/mantyx/mantyx-go-sdk"
)

type readFileArgs struct {
    Path string `json:"path" jsonschema:"description=Path to read"`
}

func main() {
    client := mantyx.NewClient(mantyx.Options{
        APIKey:        os.Getenv("MANTYX_API_KEY"),
        WorkspaceSlug: "acme-corp",
    })

    tool := mantyx.LocalTool(mantyx.LocalToolSpec{
        Name:       "read_file",
        Parameters: &readFileArgs{},
        Execute: func(ctx context.Context, raw json.RawMessage) (string, error) {
            var args readFileArgs
            if err := json.Unmarshal(raw, &args); err != nil {
                return "", err
            }
            b, err := os.ReadFile(args.Path)
            return string(b), err
        },
    })

    result, err := client.RunAgent(context.Background(), mantyx.RunSpec{
        SystemPrompt: "You are a helpful filesystem assistant.",
        Prompt:       "Read /etc/hostname and tell me what it says.",
        Tools:        []mantyx.ToolRef{tool},
    })
    if err != nil {
        log.Fatal(err)
    }
    fmt.Println(result.Text)
}
```

See [`go/README.md`](./go/README.md) for the full reference.

## Triggering a persisted MANTYX agent

Pass `agentId` (TS) / `AgentID` (Go) to run an agent that already exists in
your workspace. The server hydrates the agent's system prompt, model, and
configured tools (memory, skills, plugin tools, …) from the `Agent` row at
run time. Any `tools` you pass on the call are merged on top — typically
`local` tools the agent should be able to call back into for that run.

```ts
await client.runAgent({
  agentId: "agent_cm6abc123",
  prompt: "Summarise the latest deploy logs.",
  tools: [readLocalLogsTool],
});
```

`systemPrompt` becomes optional when `agentId` is set; if both are supplied,
the agent's stored prompt wins. The API key must be authorized for that
agent (an empty `agentIds` allowlist on the key counts as "all agents").

## Repository layout

This directory is the unified source for both SDKs in the MANTYX monorepo.
Each subfolder is **self-contained** and is what gets published to npm /
shipped as a Go module:

- [`ts/`](./ts) — TypeScript SDK (`@mantyx/sdk`) + Vitest tests + 6
  self-contained example projects under `ts/examples/`.
- [`go/`](./go) — Go SDK (`github.com/mantyx/mantyx-go-sdk`) + `httptest`
  tests + 6 example modules under `go/examples/`, each with its own
  `go.mod` and a `replace` directive that points back at this folder for
  in-tree builds.

Both ship under **Apache-2.0** and follow [Keep a Changelog](https://keepachangelog.com)
([`ts/CHANGELOG.md`](./ts/CHANGELOG.md), [`go/CHANGELOG.md`](./go/CHANGELOG.md))
plus per-SDK contributing guides
([`ts/CONTRIBUTING.md`](./ts/CONTRIBUTING.md),
[`go/CONTRIBUTING.md`](./go/CONTRIBUTING.md)) and OSS-extraction notes
([`ts/EXTRACT.md`](./ts/EXTRACT.md), [`go/EXTRACT.md`](./go/EXTRACT.md)).

## Documentation

- [`docs/agent-runs-protocol.md`](./docs/agent-runs-protocol.md) — wire protocol
  spec (HTTP routes, SSE event schema, error codes, agent spec).
- Server-side overview lives in the parent repo at
  [`docs/agent-runs.md`](../../docs/agent-runs.md) — what's persisted on the
  MANTYX side, how the runner is wired, retention, and the observability UI.

## Contributing

Open the SDK directory you care about for development setup. Both packages
have their own test suites (`pnpm test` for TypeScript, `go test ./...` for
Go), and the repo's CI runs them on every PR.

## License

Apache-2.0. See [`ts/LICENSE`](./ts/LICENSE) and [`go/LICENSE`](./go/LICENSE).
