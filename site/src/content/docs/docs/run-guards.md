---
title: Run guards
description: Loop detection and per-tool call budgets that keep autonomous agent loops bounded.
sidebar:
  order: 6
---

Long-running agent loops occasionally get stuck — the model keeps re-issuing the same `(toolName, args)` batch, or burns its turn budget hammering one expensive tool. MANTYX ships two opt-in **run guards** that intervene before either failure mode runs the run into the ground:

- **Loop detection** — fingerprints every assistant turn that emits tool calls, soft-nudges the model to pivot once it repeats itself, and forces a clean tools-disabled finalise turn if it keeps looping.
- **Tool budgets** — per-tool call caps enforced over the lifetime of the run; calls past the cap are intercepted before execution and replaced with a synthetic "budget exceeded — pivot or finalize" tool result.

Both guards have **runtime defaults** that always apply (so SDK-driven runs and platform-driven runs behave identically). You only ever need to touch them when you want to tune the thresholds, opt out for a specific run, or attach a budget to a custom tool.

## Loop detection

The pipeline tracks a canonical order-invariant `(toolName, args)` signature for every assistant turn that emits one or more tool calls. When the same signature repeats consecutively the guard fires.

| Threshold              | Default | What happens |
| ---------------------- | ------- | ------------ |
| `consecutiveThreshold` | `3`     | The pipeline skips the duplicate batch with a synthetic "you've made this exact call before" tool result and prepends a user-style **steering nudge** ("either deliver a final answer or change strategy"). The model gets the nudge before its next turn and either finalises or pivots. |
| `hardCutoffThreshold`  | `6`     | The pipeline forces a tools-disabled finalise turn so the run lands cleanly instead of churning forever. |

`hardCutoffThreshold` must be strictly greater than `consecutiveThreshold` so the soft nudge always gets a chance to land.

### Tuning the thresholds

```ts
// TypeScript
import { MantyxClient } from "@mantyx/sdk";

const result = await client.runAgent({
  systemPrompt: "...",
  prompt: "...",
  loopDetection: {
    consecutiveThreshold: 2,        // nudge after 2 identical batches
    hardCutoffThreshold:  4,        // force finalise after 4
  },
});
```

```python
# Python
result = client.run_agent(
    system_prompt="...",
    prompt="...",
    loop_detection={
        "consecutiveThreshold": 2,
        "hardCutoffThreshold": 4,
    },
)
```

```go
// Go
result, err := client.RunAgent(ctx, mantyx.RunSpec{
    SystemPrompt:  "...",
    Prompt:        "...",
    LoopDetection: mantyx.LoopDetectionThresholds(2, 4),
})
```

### Disabling the guard for a single run

Pass the literal `false` (TypeScript / Python) or the `LoopDetectionDisabled()` sentinel (Go):

```ts
await client.runAgent({
  systemPrompt: "...",
  prompt: "Iterate freely until you converge.",
  loopDetection: false,             // opt out for this run only
});
```

```python
client.run_agent(
    system_prompt="...",
    prompt="Iterate freely until you converge.",
    loop_detection=False,
)
```

```go
client.RunAgent(ctx, mantyx.RunSpec{
    SystemPrompt:  "...",
    Prompt:        "Iterate freely until you converge.",
    LoopDetection: mantyx.LoopDetectionDisabled(),
})
```

## Tool budgets

`toolBudgets` caps how many times each tool may execute over the **lifetime of the run** (across every LLM turn). Calls under the cap run normally; calls past the cap are intercepted before execution and the model receives a synthetic "budget exceeded — pivot or finalize" tool result.

Budgets are **per-tool, not pooled**: `hive_search_deals: { maxCalls: 5 }` and `hive_search_meetings: { maxCalls: 5 }` give the agent five of each, not five between them. `maxCalls: 0` disables the tool entirely (every attempt returns the synthetic body on the first try) — useful for ad-hoc denylists without rebuilding the agent's tool surface.

### Default research-tool surface

When `toolBudgets` is omitted MANTYX layers its runtime defaults on top of the spec:

| Tool                                                                                             | Default `maxCalls` |
| ------------------------------------------------------------------------------------------------ | ------------------ |
| `recall` (workspace memory hybrid search)                                                        | `4` |
| `traverse` (memory graph BFS)                                                                    | `3` |
| `hive_consult_ontology` (per-hive ontology read)                                                 | `4` |
| `hive_search_deals` / `_meetings` / `_companies` / `_people` (Sales Hive general search)         | `5` |
| `hive_search_tickets` / `_conversations` / `_accounts` (Customer Hive general search)            | `5` |
| `hive_search_releases` / `_issues` (Product Hive general search)                                 | `5` |

`hive_list_*` and `hive_get_*` are intentionally **not** capped — agents legitimately call them once per entity-of-interest, which can easily exceed any small cap during normal multi-entity reads. The loop-detection guard catches the pathological "same `(name, args)` batch over and over" case for that family without needing per-tool caps.

### Adding a budget for a custom tool

Caller overrides layer on top of the runtime defaults; when both specify a budget for the same tool, the **caller's value wins**.

```ts
await client.runAgent({
  systemPrompt: "...",
  prompt: "...",
  toolBudgets: {
    recall:        { maxCalls: 8 },        // raise the default
    expensive_api: { maxCalls: 2 },        // cap a custom tool
    scary_tool:    { maxCalls: 0 },        // disable a tool for this run
  },
});
```

```python
client.run_agent(
    system_prompt="...",
    prompt="...",
    tool_budgets={
        "recall":        {"maxCalls": 8},
        "expensive_api": {"maxCalls": 2},
        "scary_tool":    {"maxCalls": 0},
    },
)
```

```go
client.RunAgent(ctx, mantyx.RunSpec{
    SystemPrompt: "...",
    Prompt:       "...",
    ToolBudgets: mantyx.ToolBudgets{
        "recall":        {MaxCalls: 8},
        "expensive_api": {MaxCalls: 2},
        "scary_tool":    {MaxCalls: 0},
    },
})
```

### Clearing the runtime defaults

Pass an empty (but non-nil) `toolBudgets` object to start from a clean slate — useful for runs that intentionally want unbounded research:

```ts
await client.runAgent({
  systemPrompt: "...",
  prompt: "Do a deep dive on this customer.",
  toolBudgets: {},                  // no defaults; no caps
});
```

```python
client.run_agent(
    system_prompt="...",
    prompt="Do a deep dive on this customer.",
    tool_budgets={},
)
```

```go
client.RunAgent(ctx, mantyx.RunSpec{
    SystemPrompt: "...",
    Prompt:       "Do a deep dive on this customer.",
    ToolBudgets:  mantyx.ToolBudgets{},      // empty (non-nil) map
})
```

## Defaults and inheritance

For session-scoped runs the inheritance rules are the same for both fields:

- `client.createSession({ loopDetection, toolBudgets })` (TS) / `client.create_session(loop_detection=..., tool_budgets=...)` (Python) / `mantyx.SessionSpec{LoopDetection: ..., ToolBudgets: ...}` (Go) — sets the session-default applied to every subsequent message run.
- `session.send(prompt, { loopDetection, toolBudgets })` (TS) / `session.send(prompt, loop_detection=..., tool_budgets=...)` (Python) / `session.Send(ctx, prompt, mantyx.WithLoopDetection(...), mantyx.WithToolBudgets(...))` (Go) — optional per-message override; applies to that one run only and does not mutate the session's stored value.

Both fields are *additive*: omitting them keeps MANTYX's runtime defaults; passing the disable sentinel opts out; passing entries layers caller overrides on top of the defaults.

## Observability events

Every intervention emits a dedicated SSE event so the SDK can render status notes. The synthetic skip + steering nudge / tool-result already ride the normal `tool_result` and `assistant_delta` channels — you don't need to act on these events for the agent loop to keep running. Treat them as observability surface.

```jsonc
// loop-detection guard fired
{
  "seq": 9,
  "type": "loop_detected",
  "data": {
    "consecutiveCount": 3,             // length of the identical-batch streak
    "hardCutoff": false,               // false = soft nudge round; true = forced finalise
    "tools": ["recall"]                // names of the tool calls in the looping batch
  }
}

// per-tool budget exceeded
{
  "seq": 10,
  "type": "tool_budget_exceeded",
  "data": {
    "tool": "recall",                  // logical tool name
    "maxCalls": 4,                     // configured cap
    "callIndex": 5                     // which call (1-indexed) tripped the cap
  }
}
```

A single run may emit any number of these events: zero (well-behaved agents), one or more `tool_budget_exceeded` events as the model keeps reaching for capped tools, or a `loop_detected` (`hardCutoff: false`) followed by a second `loop_detected` (`hardCutoff: true`) if the model keeps looping past the soft nudge.

```ts
await client.runAgent({
  systemPrompt: "...",
  prompt: "...",
  onEvent: (ev) => {
    if (ev.type === "loop_detected") {
      console.warn(`looping on ${ev.tools.join(", ")} (×${ev.consecutiveCount})`);
    } else if (ev.type === "tool_budget_exceeded") {
      console.warn(`tool ${ev.tool} hit cap ${ev.maxCalls} on call #${ev.callIndex}`);
    }
  },
});
```

## Limits

| Constraint                                                   | Limit |
| ------------------------------------------------------------ | ----- |
| `loopDetection.consecutiveThreshold`                         | `2 ≤ n ≤ 100` |
| `loopDetection.hardCutoffThreshold`                          | `3 ≤ n ≤ 100`, must be `> consecutiveThreshold` |
| `toolBudgets` max entries                                    | `32` |
| `toolBudgets[<name>]` key length                             | `1..120` chars |
| `toolBudgets[<name>].maxCalls`                               | `0 ≤ n ≤ 1000` (functionally unlimited; `maxToolTurns: 100` fires first) |

The reference SDKs mirror these checks locally so callers see an early typed error rather than a server round-trip.

## See also

- [Streaming](/docs/streaming/) — the full SSE event vocabulary, including the `loop_detected` and `tool_budget_exceeded` observability events.
- [Wire protocol §8](/docs/wire-protocol/#8-run-guards-loopdetection-toolbudgets) — canonical spec for the wire shapes (with subsections 8.1 `loopDetection` and 8.2 `toolBudgets`).
- [Agent-runs protocol §4.6](/docs/protocol/#46-loopdetection-steering-nudge--hard-cutoff) and [§4.7](/docs/protocol/#47-toolbudgets-per-tool-call-caps) — server-side validation contract and inheritance rules for sessions.
