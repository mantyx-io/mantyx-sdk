---
title: Agent2Agent (A2A)
description: Delegate a turn to another agent — either reachable from MANTYX or only from your process.
sidebar:
  order: 4
---

An A2A tool ref hands a turn off to another agent that speaks the [Agent2Agent](https://google.github.io/A2A/) protocol. The wire format exposes two kinds depending on **who can reach the peer**:

| `kind`        | Resolved by | When to use it |
| ------------- | ----------- | -------------- |
| `a2a`         | server      | Public peer (or one in MANTYX's VPC) — MANTYX dials `agentCardUrl` over `message/send` and forwards the reply as the tool result. |
| `a2a_local`   | client      | Peer behind a VPN, on an intranet, or on the user's device — the SDK fetches the Agent Card from the URL you provide, ships it inline so MANTYX can render the tool to the model, and dials the peer for every call. MANTYX is purely a transport. |

Both kinds present the **same** `{ "message": string }` argument shape to the model, so an agent prompt that uses one transparently works with the other. A typical setup combines both: a public router agent (`a2a`) plus an intranet helper (`a2a_local`).

## Remote A2A — `mantyxA2A` / `MantyxA2A` / `mantyx_a2a`

```ts
import { mantyxA2A } from "@mantyx/sdk";

await client.runAgent({
  systemPrompt: "You are a router. Delegate billing to billing_agent.",
  prompt: "Why was I charged twice last month?",
  tools: [
    mantyxA2A({
      name: "billing_agent",
      description: "Delegate billing questions to the Acme billing agent.",
      agentCardUrl: "https://billing.acme.com/.well-known/agent-card.json",
      headers: { Authorization: `Bearer ${process.env.BILLING_TOKEN}` },
    }),
  ],
});
```

```python
from mantyx import mantyx_a2a

client.run_agent(
    system_prompt="You are a router. Delegate billing to billing_agent.",
    prompt="Why was I charged twice last month?",
    tools=[
        mantyx_a2a(
            name="billing_agent",
            description="Delegate billing questions to the Acme billing agent.",
            agent_card_url="https://billing.acme.com/.well-known/agent-card.json",
            headers={"Authorization": f"Bearer {os.environ['BILLING_TOKEN']}"},
        ),
    ],
)
```

```go
client.RunAgent(ctx, mantyx.RunSpec{
    SystemPrompt: "You are a router. Delegate billing to billing_agent.",
    Prompt:       "Why was I charged twice last month?",
    Tools: []mantyx.ToolRef{
        mantyx.MantyxA2A(mantyx.MantyxA2AOptions{
            Name:         "billing_agent",
            Description:  "Delegate billing questions to the Acme billing agent.",
            AgentCardURL: "https://billing.acme.com/.well-known/agent-card.json",
            Headers:      map[string]string{"Authorization": "Bearer " + os.Getenv("BILLING_TOKEN")},
        }),
    },
})
```

MANTYX probes the standard endpoints in order — Google ADK JSON-RPC root, A2A `/rpc`, `/message:send`, `/message/send` — sends the model's `message` argument over `message/send`, and forwards the remote agent's text reply back as the tool result.

| Field          | Required | Notes |
| -------------- | -------- | ----- |
| `name`         | yes      | Tool name surfaced to the model. Must match `^[a-zA-Z0-9_]{1,64}$`. |
| `description`  | no       | Model-facing description. Defaults to a generic delegation hint. Mention the remote agent's purpose so the model picks it for the right turn. |
| `agentCardUrl` | yes      | URL of the remote Agent Card (`/.well-known/agent-card.json`) or the JSON-RPC root the peer accepts. |
| `headers`      | no       | Flat string→string HTTP headers sent on every A2A request. Each value capped at 8 KB. |
| `contextId`    | no       | A2A `contextId` to thread multiple delegations into the same remote conversation. Omit for fresh per-call context. |

> **Headers and secrets.** The `headers` value is forwarded **as-is** by the SDK API. For long-lived credentials (refresh tokens, rotating keys) register the peer as a workspace `ExternalAgent` instead — those headers support `{{secret:NAME}}` resolution against the workspace secrets store. Use the wire-protocol `a2a` ref for short-lived per-run tokens minted by your application.

## Local A2A — `defineLocalA2A` / `LocalA2A` / `define_local_a2a`

When the peer lives somewhere only your process can reach — an intranet host, a VPN-only endpoint, the user's laptop — declare it as an `a2a_local` tool. **MANTYX does no A2A work for this kind.** The SDK owns the entire A2A relationship: it fetches the peer's [Agent Card](https://google.github.io/A2A/specification/#agent-card) from the URL you provide, ships it inline as `agentCard` (so MANTYX can describe the peer to the model), and dials the peer's `message/send` endpoint when MANTYX emits a `local_tool_call` event with `kind: "a2a_local"`.

The SDK API is **URL-only**: pass `agentCardUrl` (and optional `headers`), and the SDK takes care of everything else — fetch, cache, dispatch.

```ts
import { defineLocalA2A } from "@mantyx/sdk";

defineLocalA2A({
  name: "intranet_hr",
  agentCardUrl: "https://hr.intranet.acme/.well-known/agent-card.json",
  headers: { Authorization: `Bearer ${process.env.INTRANET_TOKEN}` },
});
```

```python
from mantyx import define_local_a2a

define_local_a2a(
    name="intranet_hr",
    agent_card_url="https://hr.intranet.acme/.well-known/agent-card.json",
    headers={"Authorization": f"Bearer {os.environ['INTRANET_TOKEN']}"},
)
```

```go
mantyx.LocalA2A(mantyx.LocalA2ASpec{
    Name:         "intranet_hr",
    AgentCardURL: "https://hr.intranet.acme/.well-known/agent-card.json",
    Headers:      map[string]string{"Authorization": "Bearer " + os.Getenv("INTRANET_TOKEN")},
})
```

| Field          | Required | Notes |
| -------------- | -------- | ----- |
| `name`         | yes      | Tool name surfaced to the model. Must match `^[a-zA-Z0-9_]{1,64}$`. |
| `description`  | no       | Optional model-facing description override. When omitted, MANTYX synthesizes one from the resolved card's `name`, `description`, and first 12 `skills`. |
| `agentCardUrl` | yes      | URL of the peer's Agent Card. Typical shape: `https://hr.intranet.acme/.well-known/agent-card.json`. The SDK fetches this on the first run and caches it. |
| `headers`      | no       | Forwarded **as-is** on both the Agent Card GET and every `message/send` POST. |

> **Don't have a card endpoint?** If your peer doesn't publish a card at a stable URL, mount a tiny HTTP handler that returns a hand-rolled JSON document with at least `{"name": "...", "url": "..."}` — that's all the SDK and MANTYX need. The A2A spec only requires `name`, with `url` recommended so MANTYX can dispatch `message/send` against it.

### Per-call lifecycle

1. **(First run / session.)** The SDK GETs `agentCardUrl` (with `headers` if you supplied them), validates the response is a JSON object with at least a `name`, and caches the parsed card.
2. The SDK ships the cached card as `agentCard` in the agent spec submitted to MANTYX.
3. The model emits a tool call against `name`.
4. MANTYX emits a `local_tool_call` SSE event with `kind: "a2a_local"`, `args: { message: string }`, and the cached Agent Card echoed back unchanged in `agentCard`.
5. The SDK speaks A2A's JSON-RPC `message/send` against `agentCard.url` (forwarding your `headers`), waits for the reply, flattens the text content of every `Part` it receives, and POSTs the result to `.../tool-results`.
6. MANTYX feeds the reply back into the model loop as the tool result.

The same `localToolTimeoutMs` budget that applies to generic [local tools](/docs/tools/local/) (default 5 minutes) applies here. Tool-result POSTs after timeout return `409 run_terminal`.

## Mixing both flavours

Because both `a2a` and `a2a_local` present the same `{ message }` shape to the model, you can swap a peer between server-resolved and client-resolved without touching the system prompt:

```ts
const tools = [
  mantyxA2A({ name: "billing_agent", agentCardUrl: "https://billing.acme.com/..." }),
  defineLocalA2A({
    name: "intranet_hr",
    agentCardUrl: "https://hr.intranet.acme/.well-known/agent-card.json",
    headers: { Authorization: `Bearer ${process.env.INTRANET_TOKEN}` },
  }),
];
```

End-to-end examples live at [`examples/a2a-tools`](/docs/examples/) for each SDK. The complete protocol contract — including the wire shape of the resolved Agent Card and the `local_tool_call.agentCard` echo — is documented in the [wire protocol reference](https://github.com/mantyx-ai/mantyx-sdk/blob/main/docs/wire-protocol.md).

## Exposing a MANTYX agent over A2A

The previous sections covered _consuming_ A2A peers from a MANTYX agent. The SDKs also let you **expose** a MANTYX agent _as_ an A2A peer, so other agents — including MANTYX agents — can discover and call it like any other Agent2Agent service.

Each SDK ships a thin wrapper around the **official** A2A library so you don't reimplement the protocol yourself:

| SDK | Module | Backed by |
| --- | --- | --- |
| TypeScript | `@mantyx/sdk/a2a-server` | [`@a2a-js/sdk`](https://www.npmjs.com/package/@a2a-js/sdk) + Express |
| Python | `mantyx.a2a_server` | [`a2a-sdk`](https://pypi.org/project/a2a-sdk/) + Starlette + uvicorn |
| Go | `github.com/mantyx-io/mantyx-sdk/go/a2asrv` | [`github.com/a2aproject/a2a-go/v2`](https://pkg.go.dev/github.com/a2aproject/a2a-go/v2) + `net/http` |

Each one exposes the same two primitives:

- a **`MantyxAgentExecutor`** that implements the official `AgentExecutor` interface and routes every A2A turn into a MANTYX agent (one-shot or session-backed) — mount it in your own Express / FastAPI / `net.http` stack;
- a **`serveAgentOverA2A` / `serve_agent_over_a2a` / `a2asrv.Serve`** helper that spins up a ready-to-run HTTP server with the Agent Card, JSON-RPC, and HTTP+JSON/REST endpoints already wired up.

Each unique A2A `contextId` is mapped to a long-lived MANTYX session by default, so multi-turn conversations share history without any extra plumbing. Pass `conversation: "stateless"` (or `ConversationStateless` in Go) to reduce every A2A request to a one-shot `runAgent` call.

### TypeScript — `@mantyx/sdk/a2a-server`

```ts
import { MantyxClient } from "@mantyx/sdk";
import { serveAgentOverA2A } from "@mantyx/sdk/a2a-server";

const client = new MantyxClient({
  apiKey: process.env.MANTYX_API_KEY!,
  workspaceSlug: process.env.MANTYX_WORKSPACE_SLUG!,
});

const handle = await serveAgentOverA2A({
  client,
  agent: { agentId: "agent_cm6abc123" }, // or { systemPrompt: "...", modelId, tools }
  port: 4000,
  agentCard: {
    name: "Acme Support",
    description: "Customer support questions.",
    protocolVersion: "0.3.0",
    version: "1.0.0",
    url: "http://localhost:4000",
    skills: [{ id: "support", name: "Support", tags: ["support"] }],
    capabilities: { streaming: true, pushNotifications: false },
    defaultInputModes: ["text"],
    defaultOutputModes: ["text"],
  },
});

console.log(`A2A peer up on ${handle.url}`);
// later:
await handle.close();
```

`@a2a-js/sdk` and `express` are declared as **optional peer dependencies**, so apps that don't expose an A2A server pay zero bundle cost. Install them on demand:

```bash
npm install @a2a-js/sdk express
```

### Python — `mantyx.a2a_server`

```python
import asyncio
from mantyx import AsyncMantyxClient
from mantyx.a2a_server import build_agent_card, serve_agent_over_a2a

async def main() -> None:
    async with AsyncMantyxClient(api_key=..., workspace_slug=...) as client:
        handle = await serve_agent_over_a2a(
            client=client,
            agent_card=build_agent_card(
                name="Acme Support",
                description="Customer support questions.",
                version="1.0.0",
                public_url="http://localhost:4000",
            ),
            agent_id="agent_cm6abc123",   # or system_prompt="...", model_id="...", tools=[...]
            port=4000,
        )
        print(f"A2A peer up on {handle.url}")
        await handle.serve_forever()  # or call handle.aclose() to stop

asyncio.run(main())
```

`a2a-sdk[http-server]` and `uvicorn` ship as the **`[a2a-server]` extra**:

```bash
pip install "mantyx-sdk[a2a-server]"
```

### Go — `github.com/mantyx-io/mantyx-sdk/go/a2asrv`

```go
import (
    mantyx "github.com/mantyx-io/mantyx-sdk/go"
    "github.com/mantyx-io/mantyx-sdk/go/a2asrv"
)

client := mantyx.NewClient(mantyx.Options{APIKey: ..., WorkspaceSlug: ...})

card := a2asrv.NewSimpleAgentCard(
    "Acme Support", "Customer support questions.", "1.0.0", "http://localhost:4000",
)

handle, err := a2asrv.Serve(ctx, a2asrv.ServeOptions{
    Client:    client,
    Agent:     a2asrv.AgentSpec{AgentID: "agent_cm6abc123"},
    AgentCard: card,
    Addr:      ":4000",
})
if err != nil { log.Fatal(err) }
defer handle.Close(context.Background())

log.Printf("A2A peer up on %s", handle.URL)
<-ctx.Done()
```

The A2A library is pulled in as a **regular dependency** of the `a2asrv` sub-package; consumers that don't import `a2asrv` don't pay any cost in their final binary.

### What the wrapper does

For each incoming A2A request:

1. Publishes a `Task` (state: `submitted`) on the first turn so streaming clients see a stable id.
2. Publishes a `working` status update.
3. Looks up the `contextId` in an in-memory LRU; opens a MANTYX session on first contact, reuses it after.
4. Forwards the A2A user message text to MANTYX as the prompt.
5. Pipes every `assistant_delta` from MANTYX into a `TaskStatusUpdateEvent` (state: `working`) carrying the chunk as a text part — clients of `message/stream` see real-time tokens.
6. On completion, publishes a final `TaskStatusUpdateEvent` (state: `completed`) with the full assistant reply.
7. Errors map to `TaskStateFailed`; explicit cancels (`tasks/cancel`) map to `TaskStateCanceled`.

The Agent Card you supply is published verbatim at `/.well-known/agent-card.json`; sessions are tagged with `metadata.a2a_context_id` for filtering in the MANTYX dashboard.

End-to-end examples live at `examples/a2a-expose` for each SDK ([TypeScript](https://github.com/mantyx-ai/mantyx-sdk/tree/main/ts/examples/a2a-expose), [Python](https://github.com/mantyx-ai/mantyx-sdk/tree/main/python/examples/a2a-expose), [Go](https://github.com/mantyx-ai/mantyx-sdk/tree/main/go/examples/a2a-expose)).
