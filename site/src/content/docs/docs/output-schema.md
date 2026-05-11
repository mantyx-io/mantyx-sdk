---
title: Output schema
description: Constrain the model's final reply to a JSON document matching a JSON Schema.
sidebar:
  order: 5
---

`outputSchema` (TypeScript / Python: `outputSchema` / `output_schema`; Go: `OutputSchema`) constrains the model's **final assistant message** to a JSON document conforming to a [JSON Schema](https://json-schema.org/). Useful when the SDK feeds the reply directly into downstream code without LLM-flavoured prose to parse out — typed extraction, agent-to-agent handoffs, function-style RPCs, etc.

The terminal `result` event still carries the reply as `text: string`, but that string is guaranteed-parseable JSON that matches the schema you supply. Each SDK ships a helper (`parseRunOutput` / `parse_run_output` / `ParseRunOutput`) that turns it into a typed value with a clean error path on the rare occasions a model still returns non-JSON.

## Wire shape

```jsonc
"outputSchema": {
  "name":   "weather_report",        // optional; default "output"; /^[a-zA-Z0-9_-]{1,64}$/
  "schema": { /* JSON Schema */ }    // required, root must be a JSON object
}
```

| Field    | Type   | Required | Notes |
| -------- | ------ | -------- | ----- |
| `name`   | string | no       | Stable identifier the server forwards to providers (OpenAI `text.format.name`, Anthropic synthetic-tool name). Defaults to `"output"`. |
| `schema` | object | yes      | JSON Schema describing the assistant text. Root must be a JSON object — most providers reject array / scalar roots in structured-output mode. Shipped verbatim; MANTYX does not validate the schema's contents (the provider does). |

Server-side limits (mirrored locally by every reference SDK so you get an early typed error):

| Constraint                                   | Limit |
| -------------------------------------------- | ----- |
| Serialised JSON size of the whole `outputSchema` | ≤ 32 KB |
| `name` regex                                 | `/^[a-zA-Z0-9_-]{1,64}$/` |
| `schema` shape                               | non-`null`, non-array JSON object |

## Per provider

| Provider                       | How the schema is enforced |
| ------------------------------ | -------------------------- |
| OpenAI Responses (o-series, GPT-5.x, …) | `text.format = { type: "json_schema", strict: true, name, schema }` on every `completeTurn` (compatible with tool calls). |
| Gemini ≥ 2.5                   | `responseMimeType: "application/json"` + `responseJsonSchema` on no-tools turns (Gemini rejects schemas alongside `functionDeclarations`). |
| Anthropic / Bedrock-Anthropic  | Synthetic `final_report` tool whose `input_schema` is the supplied schema; `tool_choice` is forced on the no-tools finishing turn. The tool's input is surfaced as the assistant text. |
| xAI Grok, others               | Ignored — the model returns plain text. |

`outputSchema` is independent of [`reasoningLevel`](/docs/reasoning/): the model can think extensively *and* emit JSON.

## Per-SDK syntax

### TypeScript

```ts
import { z } from "zod";
import { MantyxClient, parseRunOutput } from "@mantyx/sdk";

const client = new MantyxClient({ apiKey: "...", workspaceSlug: "acme" });

const WeatherJsonSchema = {
  type: "object",
  properties: {
    city:          { type: "string" },
    temperature_c: { type: "number" },
  },
  required: ["city", "temperature_c"],
  additionalProperties: false,
} as const;

const Weather = z.object({
  city: z.string(),
  temperature_c: z.number(),
});

const result = await client.runAgent({
  systemPrompt: "Return the weather as JSON.",
  prompt: "What's the weather in San Francisco right now?",
  outputSchema: { name: "weather_report", schema: WeatherJsonSchema },
});

const report = parseRunOutput(result, (v) => Weather.parse(v));
//    ^? { city: string; temperature_c: number }
```

`parseRunOutput<T>(result, validator?)` JSON-parses `result.text`, runs the optional validator (zod `.parse`, Ajv, anything callable), and throws a typed `MantyxParseError` on failure. The original raw text is preserved on `err.text` for logging.

### Python

```python
from pydantic import BaseModel
from mantyx import MantyxClient, parse_run_output

WEATHER_SCHEMA = {
    "type": "object",
    "properties": {
        "city":          {"type": "string"},
        "temperature_c": {"type": "number"},
    },
    "required": ["city", "temperature_c"],
    "additionalProperties": False,
}

class Weather(BaseModel):
    city: str
    temperature_c: float

client = MantyxClient(api_key="...", workspace_slug="acme")

result = client.run_agent(
    system_prompt="Return the weather as JSON.",
    prompt="What's the weather in San Francisco right now?",
    output_schema={"name": "weather_report", "schema": WEATHER_SCHEMA},
)

report = parse_run_output(result, Weather.model_validate)
# report is a fully-typed Weather instance.
```

The `[OutputSchema]` TypedDict from `mantyx.tools` is the type alias for the dict shape; pass any `Mapping[str, Any]` that conforms.

### Go

`OutputSchema.Schema` accepts either a `map[string]any` / `json.RawMessage`
JSON Schema *or* a Go struct (or pointer-to-struct). When given a struct,
the SDK reflects it via `google/jsonschema-go` — the same path
`LocalToolSpec.Parameters` uses — so a single Go type can drive both the
schema you ship to the provider and the typed receive shape you decode
into:

```go
import (
    "context"
    "errors"
    mantyx "github.com/mantyx-io/mantyx-sdk/go"
)

client := mantyx.NewClient(mantyx.Options{APIKey: "...", WorkspaceSlug: "acme"})

type WeatherReport struct {
    City         string  `json:"city"          jsonschema:"City the report is for"`
    TemperatureC float64 `json:"temperature_c" jsonschema:"Current temperature in Celsius"`
}

result, err := client.RunAgent(ctx, mantyx.RunSpec{
    SystemPrompt: "Return the weather as JSON.",
    Prompt:       "What's the weather in San Francisco right now?",
    OutputSchema: &mantyx.OutputSchema{
        Name:   "weather_report",
        Schema: &WeatherReport{},
    },
})
if err != nil { /* ... */ }

var report WeatherReport
if err := mantyx.ParseRunOutput(result, &report); err != nil {
    var pe *mantyx.ParseError
    if errors.As(err, &pe) {
        log.Printf("model returned non-JSON text: %q", pe.Text)
    }
    return err
}
```

If you'd rather keep the schema explicit, the same call also accepts a
hand-rolled `map[string]any` (or `json.RawMessage`) containing the full
JSON Schema — both shapes are passed through verbatim:

```go
weatherSchema := map[string]any{
    "type": "object",
    "properties": map[string]any{
        "city":          map[string]any{"type": "string"},
        "temperature_c": map[string]any{"type": "number"},
    },
    "required":             []any{"city", "temperature_c"},
    "additionalProperties": false,
}

result, err := client.RunAgent(ctx, mantyx.RunSpec{
    SystemPrompt: "Return the weather as JSON.",
    Prompt:       "What's the weather in San Francisco right now?",
    OutputSchema: &mantyx.OutputSchema{Name: "weather_report", Schema: weatherSchema},
})
```

## Defaults and inheritance

`outputSchema` works on **both ephemeral runs** (`systemPrompt`-defined) and **`agentId`-backed runs** — the runner applies it to whichever `AgentSpec` it built for the run. When the field is omitted, the run returns unconstrained plain text as before.

For session-scoped runs the inheritance rules are:

- `client.createSession({ outputSchema })` (TS) / `client.create_session(output_schema=...)` (Python) / `mantyx.SessionSpec{OutputSchema: ...}` (Go) — sets the session-default applied to every subsequent message run.
- `session.send(prompt, { outputSchema })` (TS) / `session.send(prompt, output_schema=...)` (Python) / `session.Send(ctx, prompt, mantyx.WithOutputSchema(...))` (Go) — optional per-message override; applies to that one run only and does not mutate the session's stored value.

```ts
const session = await client.createSession({
  systemPrompt: "...",
  outputSchema: { schema: WeatherJsonSchema }, // default for every turn
});

await session.send("Weather in Tokyo?");                                 // matches WeatherJsonSchema
await session.send("Now summarise our chat in plain prose.", {
  outputSchema: undefined as never,                                      // (illustrative)
});
```

> **Tip:** to disable structured output for a single turn in a structured session, simply omit the option — the per-message override applies *only* when explicitly set; it does not "unset" the session default. If you need plain-text mid-session today, run that turn through a stateless `runAgent` on the same client.

## Error handling

Even though the server enforces JSON shape via the provider, transient model errors (refusal text, truncation under `max_tokens` pressure, exotic Unicode normalisation) can still occasionally produce a string that fails to parse. The reference SDKs:

1. Pass the schema through unchanged from your code to the wire.
2. Run a `JSON.parse` / `json.loads` / `json.Unmarshal` on the terminal `result.text` only when you call `parseRunOutput` / `parse_run_output` / `ParseRunOutput`.
3. Re-validate against your source-of-truth Zod / Pydantic / typed-struct schema.
4. Surface a typed `MantyxParseError` (`*ParseError` in Go) carrying the raw `text` so you can log it for debugging.

```ts
import { MantyxParseError } from "@mantyx/sdk";

try {
  const report = parseRunOutput(result, Weather.parse.bind(Weather));
  // happy path
} catch (err) {
  if (err instanceof MantyxParseError) {
    console.warn("model returned non-conformant text:", err.text);
  }
  throw err;
}
```

### Truncation salvage (`errorClass: "truncation"`)

When the model hits the provider's output-token budget mid-JSON, MANTYX
**does not discard** the bytes that already streamed. Instead the run
terminates with a `MantyxRunError` (`*RunError` in Go) whose
`errorClass === "truncation"` carries the partial output on
`partialText` (`partial_text` / `PartialText`). Catch this case before
`parseRunOutput` so callers see a clear "truncated reply — JSON likely
incomplete" surface instead of a bare JSON parse failure:

```ts
import { MantyxRunError } from "@mantyx/sdk";

try {
  const result = await client.runAgent({ /* … outputSchema */ });
  return parseRunOutput(result, Weather.parse.bind(Weather));
} catch (err) {
  if (err instanceof MantyxRunError && err.errorClass === "truncation") {
    // `err.partialText` carries the raw bytes; do NOT auto-fallback to it
    // as the final answer, since the JSON object is almost certainly
    // unclosed. Surface a "truncated — please rephrase or raise the budget"
    // banner instead.
    console.warn("output truncated:", err.partialText);
    throw err;
  }
  throw err;
}
```

The same salvage is also persisted on the run row — `GET /agent-runs/{id}`
returns `{ status: "failed", finalText: "<partial JSON>", error: "Model
output was truncated …", failureReason: { errorClass: "truncation",
finishReason: "max_tokens" } }` — so SDKs that re-fetch the row after a
reconnect see both pieces consistently. See
[Wire protocol §4.7](/docs/wire-protocol/#47-terminal-events) for the
full truncation contract.

## See also

- [`reasoningLevel`](/docs/reasoning/) — independent dial for thinking effort; combine the two to get deep-reasoning JSON outputs.
- [Run guards](/docs/run-guards/) — loop detection and per-tool budgets that protect long agent loops, including the JSON-finalising turn that backs `outputSchema` under the hood.
- [Local tools — structured output](/docs/tools/local/#declaring-an-outputschema) — the same JSON Schema affordance applied to a single local tool's return value (forwarded as `outputSchema` on the wire). Pair with [`longRunning`](/docs/tools/local/#long-running-tools) when a tool may return a `pending` status and you do the polling yourself.
- [Wire protocol §7](/docs/wire-protocol/#7-outputschema-structured-final-reply) — the canonical spec for the run-level `outputSchema` wire shape, per-provider mapping, and SDK guidance.
- [Agent-runs protocol §4.5](/docs/protocol/#45-outputschema-structured-final-reply) — server-side validation contract and inheritance rules for sessions.
