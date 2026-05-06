---
title: Overview
description: What MANTYX is and how the SDKs fit in.
sidebar:
  order: 1
---

[MANTYX](https://mantyx.com) is an **agent operating system**: it owns the LLM loop, the workspace tool catalog, memory, skills, and persisted observability. The SDKs let you drive that runtime from your own process — define ephemeral agents inline, trigger persisted MANTYX agents by id, and seamlessly mix **remote** workspace tools with **local** tools that run in your process and shuttle results back over the agent loop.

## What you can do with the SDKs

- **Run an ephemeral agent** — describe a system prompt, model, and tool list on the call site. MANTYX runs the loop and streams results back.
- **Trigger a persisted MANTYX agent (`agentId`)** — reuse an agent that already lives in your workspace (with its system prompt, model, memory, skills, and tool list) and optionally merge in extra `local` tools for that single run.
- **Maintain conversational sessions** — multi-turn agent runs whose history persists on the server, with optional per-turn tool refresh.
- **Mix remote and local tools** — server-resolved (`mantyx`, `mantyx_plugin`, `a2a`, `mcp`) or client-resolved (`local`, `a2a_local`, `mcp_local`). Connect public peers, internal services, on-device MCP servers, and your own functions in the same agent.
- **Delegate across agents** — call out to other [Agent2Agent](/docs/tools/a2a/) peers, whether MANTYX can reach them or only your SDK can.
- **Connect MCP servers** — expose every tool of an [MCP server](/docs/tools/mcp/) (remote Streamable HTTP or local stdio) to the loop in one go.
- **Stream tokens** — assistant deltas, thinking deltas, server tool results, local tool calls, and the terminal `result` event over SSE.
- **Pick a model** — choose a workspace BYOK provider, a specific vendor model, or a platform-hosted offering via a unified `modelId` string.
- **Tune thinking effort** — set [`reasoningLevel`](/docs/reasoning/) per run (`"off" | "low" | "medium" | "high"` or `0–100`) and MANTYX maps it onto each provider's native dial.
- **Constrain replies to JSON** — pass an [`outputSchema`](/docs/output-schema/) (a JSON Schema) and the model's final message is guaranteed to be parseable JSON; each SDK ships a `parseRunOutput` helper that decodes it into your own typed value.
- **Tag for observability** — attach a flat `metadata` KV (e.g. `{ customer: "acme", env: "prod" }`) to runs and sessions so your team can filter the dashboard by them.

## Three first-party SDKs

| | TypeScript | Go | Python |
| --- | --- | --- | --- |
| Package | `@mantyx/sdk` | `github.com/mantyx-io/mantyx-go-sdk` | `mantyx-sdk` |
| Install | `npm install @mantyx/sdk` (bundles `zod` + `@modelcontextprotocol/sdk`) | `go get github.com/mantyx-io/mantyx-go-sdk` | `pip install mantyx-sdk` |
| Min runtime | Node.js 18.17+ | Go 1.24+ | Python 3.10+ |
| Local tool params | [Zod](https://zod.dev) schema | tagged Go struct | [Pydantic v2](https://docs.pydantic.dev) model |

All three speak the same wire protocol (see [Wire protocol](/docs/protocol/)) and expose the same conceptual surface — `runAgent`, `streamAgent`, `createSession`, `resumeSession`, `endSession`, `listModels`, `cancelRun` — adapted to language conventions.

## Next steps

- [Authentication](/docs/getting-started/authentication/) — how to generate an API key.
- [Quickstart](/docs/quickstart/) — your first run, in any of the three SDKs.
- [Wire protocol](/docs/protocol/) — the HTTP + SSE spec, the source of truth for third-party clients.
