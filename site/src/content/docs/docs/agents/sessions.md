---
title: Sessions
description: Multi-turn conversations whose message history persists on the server.
sidebar:
  order: 3
---

Sessions own the agent spec (system prompt, model, tool defs) and the full message history. Each `send` is a run scoped to the session.

```ts
const session = await client.createSession({
  systemPrompt: "You are a friendly REPL.",
  tools: [
    defineLocalTool({
      name: "today",
      description: "Get today's date as ISO 8601.",
      parameters: z.object({}),
      execute: () => new Date().toISOString().slice(0, 10),
    }),
  ],
});

const r1 = await session.send("What day is it?");
console.log(r1.text);

const r2 = await session.send("And what about tomorrow?");
console.log(r2.text);

await session.end();
```

## Resuming a session

A session lives on the server. Resuming from a different process re-binds your local tool handlers — pass them via `resumeSession`:

```ts
const session = await client.resumeSession(sessionId, {
  tools: [
    defineLocalTool({
      name: "today",
      parameters: z.object({}),
      execute: () => new Date().toISOString().slice(0, 10),
    }),
  ],
});
```

Local tool **handlers** are not persisted: the session stores definitions (name, schema, description) so that a restarted SDK can re-bind handlers and keep going. If you don't pass `tools` to `resumeSession`, the session uses whatever it had at create time — which means subsequent `send` calls will fail to dispatch local tools because there are no handlers in this process.

## Session metadata

Anything you pass as `metadata` at session creation is inherited by every run created via `session.send`. See [Metadata](/docs/metadata/) for the validation rules and the per-message override pattern.

```ts
const session = await client.createSession({
  systemPrompt: "...",
  metadata: { customer: "acme", env: "prod" },
});

await session.send("trace this turn", {
  metadata: { trace_id: "trace_abc" }, // run-level keys win
});
```

## Persisted-agent sessions

Pass `agentId` to create a session backed by a persisted MANTYX agent — see [Persisted agents](/docs/agents/persisted/) for the full semantics.

```ts
const session = await client.createSession({
  agentId: "agent_cm6abc123",
});
const r = await session.send("Summarise yesterday's tickets.");
```
