---
title: Metadata
description: Tag runs and sessions with a flat KV for dashboard filtering.
sidebar:
  order: 7
---

Attach a flat string→string KV to runs and sessions so your team can filter the dashboard by it (Agent runs → "Metadata" filter):

```ts
// One-shot run
await client.runAgent({
  systemPrompt: "...",
  prompt: "...",
  metadata: { customer: "acme", env: "prod", workflow: "support_triage" },
});

// Session — every run created via `session.send` inherits these tags
const session = await client.createSession({
  systemPrompt: "...",
  metadata: { customer: "acme", env: "prod" },
});

// Per-message override; merged on top of the session's metadata
// (run-level keys win)
await session.send("trace this turn", {
  metadata: { trace_id: "trace_abc" },
});
```

```python
client.run_agent(
    system_prompt="...",
    prompt="...",
    metadata={"customer": "acme", "env": "prod", "workflow": "support_triage"},
)

session = client.create_session(
    system_prompt="...",
    metadata={"customer": "acme", "env": "prod"},
)
session.send("trace this turn", metadata={"trace_id": "trace_abc"})
```

```go
_, _ = client.RunAgent(ctx, mantyx.RunSpec{
    SystemPrompt: "...",
    Prompt:       "...",
    Metadata:     map[string]string{"customer": "acme", "env": "prod"},
})

session, _ := client.CreateSession(ctx, mantyx.SessionSpec{
    SystemPrompt: "...",
    Metadata:     map[string]string{"customer": "acme", "env": "prod"},
})
_, _ = session.Send(ctx, "trace this turn",
    mantyx.WithMetadata(map[string]string{"trace_id": "trace_abc"}),
)
```

## Validation rules

Server-side, enforced as `400 invalid_request`:

| Constraint | Limit |
| --- | --- |
| Max entries | 16 |
| Key pattern | `^[A-Za-z0-9._-]{1,64}$` |
| Value type / length | string ≤ 256 chars |
| Serialized JSON size | ≤ 4 KB |

## Inheritance rules

- `POST /agent-sessions { metadata }` — sets the session's metadata; this is inherited by every run created through `POST /agent-sessions/:id/messages`.
- `POST /agent-sessions/:id/messages { metadata }` — optional per-message override. The server snapshots `session.metadata ⊕ override` (run-level keys win) onto the run row at creation time. Later edits to the session metadata do **not** retroactively rewrite past runs.

Metadata is returned on every read endpoint and surfaced in the dashboard filters. See [Agent-runs protocol §4.8](/docs/protocol/#48-metadata-developer-supplied-kv-for-filtering) for the canonical spec.

## See also

- [Run guards](/docs/run-guards/) — loop detection and per-tool budgets follow the same session-default + per-message override pattern as metadata.
