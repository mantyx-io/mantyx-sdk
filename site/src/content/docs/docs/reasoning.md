---
title: Reasoning level
description: Tune how much extended-thinking effort the model spends per turn.
sidebar:
  order: 4
---

`reasoningLevel` (TypeScript / Python: `reasoningLevel` / `reasoning_level`; Go: `ReasoningLevel`) controls how much extended-thinking / reasoning effort the model spends on each turn. MANTYX maps the same value onto every supported provider so you can dial up "deeper thinking" without writing provider-specific code.

| Provider                    | Maps to |
| --------------------------- | ------- |
| OpenAI Responses (o-series, GPT-5.x) | `reasoning.effort` (ignored on non-reasoning models, including xAI Grok). |
| Gemini 3+                   | `thinkingConfig.thinkingLevel`; pre-Gemini-3 models consume the equivalent `thinkingBudget` token count. |
| Anthropic / Bedrock-Anthropic | Extended thinking with a budget that scales with strength (≈512 tokens at `low` → ≈8000 at `high`). |

## Two input shapes

Both shapes are accepted on the wire. Pick whichever feels most natural for your call site.

| Form    | Values                                          | Notes |
| ------- | ----------------------------------------------- | ----- |
| String  | `"off"`, `"low"`, `"medium"`, `"high"`          | Snaps to the same anchors the web composer uses (Fast=30, Moderate=50, Smart=80; off=0). |
| Number  | integer `0`–`100`                               | Pass-through. `0` explicitly disables provider thinking even on reasoning models. |

## Per-SDK syntax

```ts
// TypeScript — string anchor
await client.runAgent({
  systemPrompt: "...",
  prompt: "Plan a multi-week migration.",
  reasoningLevel: "high",
});

// Or a numeric value in [0, 100]
await client.runAgent({ /* ... */ reasoningLevel: 80 });
```

```python
# Python — string anchor or int in [0, 100]
client.run_agent(system_prompt="...", prompt="...", reasoning_level="medium")
client.run_agent(system_prompt="...", prompt="...", reasoning_level=80)
```

```go
// Go — typed builders for the four anchors, plus an integer escape hatch.
client.RunAgent(ctx, mantyx.RunSpec{
    SystemPrompt:   "...",
    Prompt:         "Plan a multi-week migration.",
    ReasoningLevel: mantyx.ReasoningHigh(),
})

// Or:
client.RunAgent(ctx, mantyx.RunSpec{
    /* ... */
    ReasoningLevel: mantyx.ReasoningEffort(80),
})
```

| Builder                       | Wire value | Notes |
| ----------------------------- | ---------- | ----- |
| `mantyx.ReasoningOff()`       | `"off"`    | Disables provider thinking. |
| `mantyx.ReasoningLow()`       | `"low"`    | Web composer's "Fast" preset. |
| `mantyx.ReasoningMedium()`    | `"medium"` | Web composer's "Moderate" preset. |
| `mantyx.ReasoningHigh()`      | `"high"`   | Web composer's "Smart" preset. |
| `mantyx.ReasoningEffort(n)`   | `n`        | Integer in `[0, 100]`. `0` disables thinking explicitly. |

## Defaults and inheritance

When `reasoningLevel` is omitted MANTYX falls back to the agent's default:

- **Ephemeral specs** — thinking is off.
- **Persisted agents (`agentId`)** — the persisted `Agent` configuration wins.

For session-scoped runs the inheritance rules are:

- `client.createSession({ reasoningLevel })` sets the session-default applied to every subsequent `session.send` run.
- `session.send(prompt, { reasoningLevel })` (TS) / `session.send(prompt, reasoning_level=...)` (Python) / `session.Send(ctx, prompt, mantyx.WithReasoningLevel(...))` (Go) is an optional per-message override; it applies to that one run only and does not mutate the session's stored value.

```ts
const session = await client.createSession({
  systemPrompt: "...",
  reasoningLevel: "low", // baseline for every turn
});

await session.send("Quick check: is the migration ready?");
await session.send("Now plan the rollout in detail.", {
  reasoningLevel: "high", // burst of thinking for one turn only
});
```

## Streaming thinking deltas

When `reasoningLevel > 0` (and the active provider exposes thought parts — Anthropic extended thinking, Gemini `includeThoughts`, OpenAI `reasoning_content` on reasoning models), MANTYX emits `thinking_delta` events on the SSE stream alongside the regular `assistant_delta` tokens. Most UIs hide them by default; see [Streaming](/docs/streaming/) for the full event vocabulary.

## When to use it

- **`"off"` / `0`** — fastest, cheapest. The default. Good for routing, summarisation, simple tool calls.
- **`"low"` / 30** — light thinking. Helps on multi-step reasoning at minimal latency cost.
- **`"medium"` / 50** — balanced default for non-trivial tasks.
- **`"high"` / 80–100** — deep planning, debugging, math. Latency and token cost rise meaningfully.

Non-reasoning models (most fast-tier offerings, xAI Grok) silently ignore the value, so it's safe to set unconditionally.

## See also

- [`outputSchema`](/docs/output-schema/) — independent dial that constrains the model's final reply to a JSON document. Combine the two for deep-reasoning JSON outputs (`reasoningLevel: "high"` + `outputSchema: { schema }`).
