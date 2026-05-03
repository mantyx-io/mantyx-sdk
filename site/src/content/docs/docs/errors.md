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
| `MantyxRunError` | The agent loop terminated with `result.subtype != "success"` |
| `MantyxToolError` | A local tool handler threw or timed out |

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
  if (e instanceof MantyxAuthError) console.error("rotate the API key");
  else if (e instanceof MantyxRunError) console.error("agent failed:", e.subtype);
  else throw e;
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
    print("agent failed:", e.subtype)
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
        log.Printf("agent failed: %s", run.Code)
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
