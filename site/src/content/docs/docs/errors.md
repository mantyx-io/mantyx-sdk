---
title: Errors
description: Typed error hierarchy raised by the SDKs.
sidebar:
  order: 6
---

All three SDKs raise a small typed hierarchy so you can `try` / `except` (or check `err.(*mantyx.RunError)`) at the granularity you need.

| Error | When |
| --- | --- |
| `MantyxError` (base) | Any other SDK-raised condition |
| `MantyxAuthError` | `401` / `403` from the server (bad API key, wrong workspace, agent not in allowlist) |
| `MantyxNetworkError` | Transport-layer failures (DNS, TCP reset, timeout) |
| `MantyxRunError` | The agent loop terminated with a non-success `result`, a terminal `error` event, or a `cancelled` |
| `MantyxToolError` | A local tool handler threw or timed out |

## `MantyxRunError` triage attributes

When the run terminates via a terminal `error` event (e.g. the model
truncated mid-reply, the upstream provider returned a rate limit, the
local-tool POST timed out), the SDKs surface the wire-level triage
attributes on the run error so callers can render UI banners and drive
retry policy without re-parsing the human-readable message:

| Attribute (TS / Python / Go) | Meaning |
| --- | --- |
| `errorClass` / `error_class` / `ErrorClass` | Canonical category — `"rate_limit"`, `"overloaded"`, `"server"`, `"context_window"`, `"truncation"`, `"invalid_request"`, `"auth"`, `"timeout"`, `"local_timeout"`, `"upstream_deadline"`, or `"unknown"`. New categories may land additively. |
| `finishReason` / `finish_reason` / `FinishReason` | Canonical lowercase provider stop reason (`"max_tokens"`, `"refusal"`, `"malformed_function_call"`, …). Mirrors the last `assistant_message` event's `finishReason`. |
| `partialText` / `partial_text` / `PartialText` | **Best-effort raw bytes** the model emitted before the failure. For `outputSchema` runs this is usually incomplete JSON that will fail `JSON.parse` — treat it as diagnostic data, never as a schema-conformant reply. |
| `retryable` / `retryable` / `Retryable` | Coarse retry hint inherited from the pipeline's classifier. Informational; the SDK does not retry on your behalf. |

All four are optional: older runners that haven't classified the failure
yet, or non-terminal `error` paths, leave them unset (`undefined` in TS,
`None` in Python, zero-values / `nil` in Go).

The `errorClass` taxonomy and the truncation salvage contract are both
described in detail in [Wire protocol §4.7](/docs/wire-protocol/#47-terminal-events)
and [Agent-runs protocol §7](/docs/protocol/#7-sse-stream).

## TypeScript

```ts
import {
  MantyxAuthError,
  MantyxNetworkError,
  MantyxRunError,
  MantyxToolError,
} from "@mantyx/sdk";

try {
  await client.runAgent({ systemPrompt: "...", prompt: "..." });
} catch (e) {
  if (e instanceof MantyxAuthError) {
    console.error("rotate the API key");
  } else if (e instanceof MantyxRunError) {
    if (e.errorClass === "truncation") {
      // The model ran out of output budget mid-reply; `e.partialText` carries
      // the raw bytes emitted so far (typically incomplete JSON).
      console.warn("truncated reply — JSON likely incomplete:", e.partialText);
    } else {
      console.error("agent failed:", e.errorClass ?? e.subtype, "—", e.message);
    }
  } else {
    throw e;
  }
}
```

## Python

```python
from mantyx import MantyxAuthError, MantyxRunError

try:
    client.run_agent(system_prompt="...", prompt="...")
except MantyxAuthError:
    print("rotate the API key")
except MantyxRunError as e:
    if e.error_class == "truncation":
        # The model ran out of output budget mid-reply; `e.partial_text`
        # carries the raw bytes emitted so far (typically incomplete JSON).
        print("truncated reply — JSON likely incomplete:", e.partial_text)
    else:
        print("agent failed:", e.error_class or e.subtype, "—", e.message)
```

## Go

```go
result, err := client.RunAgent(ctx, mantyx.RunSpec{...})
if err != nil {
    var auth *mantyx.AuthError
    var run *mantyx.RunError
    switch {
    case errors.As(err, &auth):
        log.Println("rotate the API key")
    case errors.As(err, &run):
        if run.ErrorClass == "truncation" {
            // The model ran out of output budget mid-reply; `run.PartialText`
            // carries the raw bytes emitted so far (typically incomplete JSON).
            log.Printf("truncated reply — JSON likely incomplete: %s", run.PartialText)
        } else {
            log.Printf("agent failed (%s): %s", run.Code, run.Message)
        }
    default:
        log.Fatal(err)
    }
}
```

## Common server error codes

| Code | HTTP | Notes |
| --- | --- | --- |
| `unauthorized` | 401 | Missing/invalid API key |
| `forbidden` | 403 | API key not authorized for this agent |
| `not_found` | 404 | Workspace, run, or session unknown |
| `invalid_request` | 400 | Body failed validation |
| `invalid_model` | 400 | `modelId` couldn't be resolved |
| `unknown_tool_use` | 404 | Tool-result for an unknown `toolUseId` |
| `run_terminal` | 409 | Tool-result after run finished |
| `rate_limited` | 429 | Per-API-key sliding window |

The full list (and run-level `result.subtype` codes) lives in [Wire protocol](/docs/protocol/) §10.
