# MANTYX Go SDK (`github.com/mantyx-io/mantyx-sdk/go`)

The official Go SDK for the [MANTYX](https://mantyx.com) agent runtime.
Define ephemeral agents that mix server-side MANTYX tools with
locally-executed tools, run them remotely, and stream events back into your
program.

- LLM loop runs on MANTYX (BYOK or platform-hosted models).
- Server-resolved tools (`mantyx`, `mantyx_plugin`, `a2a`, `mcp`) execute
  inside MANTYX — including remote Agent2Agent peers and remote MCP servers.
- Client-resolved tools (`local`, `a2a_local`, `mcp_local`) execute in *your*
  process; the SDK shuttles arguments and results over an SSE stream + a
  tool-result POST.
- Tunable provider thinking via `ReasoningLevel` (string anchors or 0–100).
- One-shot runs and multi-turn sessions, both with persisted observability.
- Authenticated with a single workspace API key.

For background, see the [agent-runs protocol spec](./docs/agent-runs-protocol.md).

## Install

```bash
go get github.com/mantyx-io/mantyx-sdk/go@latest
```

Requires Go 1.24+. Third-party runtime dependencies:

- [`github.com/google/jsonschema-go`](https://github.com/google/jsonschema-go)
  (BSD-3-Clause) — converts Go structs into JSON Schema for local tool parameters.
- [`github.com/modelcontextprotocol/go-sdk`](https://github.com/modelcontextprotocol/go-sdk)
  (Apache-2.0) — drives the Streamable HTTP and stdio transports for
  `LocalMcp`. The SDK is the implementation under `mantyx.LocalMcp`; you
  don't need to import it yourself.

## Quickstart

```go
package main

import (
    "context"
    "fmt"
    "log"
    "os"

    mantyx "github.com/mantyx-io/mantyx-sdk/go"
)

type readFileArgs struct {
    Path string `json:"path" jsonschema:"required"`
}

func main() {
    client := mantyx.NewClient(mantyx.Options{
        APIKey:        os.Getenv("MANTYX_API_KEY"),
        WorkspaceSlug: os.Getenv("MANTYX_WORKSPACE_SLUG"),
    })

    ctx := context.Background()
    result, err := client.RunAgent(ctx, mantyx.RunSpec{
        SystemPrompt: "You are a helpful assistant.",
        Prompt:       "Read /etc/hostname and summarize what it says.",
        Tools: []mantyx.ToolRef{
            mantyx.LocalTool(mantyx.LocalToolSpec{
                Name:        "read_file",
                Description: "Read a UTF-8 file from the local filesystem.",
                Parameters:  &readFileArgs{},
                Execute: func(ctx context.Context, args readFileArgs) (string, error) {
                    data, err := os.ReadFile(args.Path)
                    if err != nil {
                        return "", err
                    }
                    return string(data), nil
                },
            }),
            // Reference a MANTYX workspace tool by id:
            mantyx.MantyxTool("tool_cm6abc123"),
        },
    })
    if err != nil {
        log.Fatal(err)
    }
    fmt.Println(result.Text)
}
```

The SDK opens an SSE stream to MANTYX, listens for `local_tool_call` events,
calls the matching local handler, and POSTs the result back. The server
keeps running the agent loop until it produces a final reply.

### Typed returns

`Execute` accepts a typed second return value too — the SDK
`json.Marshal`s it on your behalf, so the same Go type drives both your
typed handler return *and* the JSON the model receives:

```go
type ResolveIDsArgs struct {
    IDs  []int  `json:"ids"  jsonschema:"Numeric IDs to resolve."`
    Type string `json:"type" jsonschema:"Entity kind: user|project|plan|..."`
}
type ResolveIDsResult struct {
    Labels map[int]string `json:"labels"`
}

mantyx.LocalTool(mantyx.LocalToolSpec{
    Name:        "resolve_ids",
    Description: "Resolve numeric IDs to human-readable labels.",
    Parameters:  &ResolveIDsArgs{},
    Execute: func(ctx context.Context, args *ResolveIDsArgs) (*ResolveIDsResult, error) {
        out, err := lookup(ctx, args.IDs, args.Type)
        if err != nil {
            return nil, err
        }
        return &ResolveIDsResult{Labels: out}, nil
    },
})
```

The Parameters / Execute / R triple is type-checked end-to-end by the
Go compiler. `string` returns are still forwarded verbatim, and
`json.RawMessage` returns are forwarded as raw JSON bytes for handlers
that have already produced JSON themselves.

## Triggering a persisted MANTYX agent

Set `AgentID` on `RunSpec` (or `SessionSpec`) to run an agent that already
exists in your workspace. The server hydrates the agent's system prompt,
model, and server-side tools (memory, skills, plugin tools, …) from the
`Agent` row at run time. Any `Tools` you pass are **merged on top** —
typically `LocalTool` refs you want the agent to be able to call back into
for this specific run.

```go
result, err := client.RunAgent(ctx, mantyx.RunSpec{
    AgentID: "agent_cm6abc123",
    Prompt:  "Pull the latest deploy logs and summarise them.",
    Tools: []mantyx.ToolRef{
        mantyx.LocalTool(mantyx.LocalToolSpec{
            Name: "read_local_file",
            Execute: func(ctx context.Context, args struct{ Path string `json:"path"` }) (string, error) {
                b, err := os.ReadFile(args.Path)
                if err != nil { return "", err }
                return string(b), nil
            },
        }),
    },
})
```

Notes:

- `SystemPrompt` becomes optional when `AgentID` is set; if both are
  supplied, the agent's stored prompt wins.
- `ModelID` is also optional: omit it to use the agent's configured LLM
  provider, or pass it to override the model for this run.
- The API key must be authorized for the agent (an empty `agentIds` allow-
  list on the key counts as "all agents in the workspace"). Otherwise the
  call returns `403`.

## Agent2Agent delegation

Hand a turn off to another agent — either a remote peer MANTYX dials
directly (`MantyxA2A`) or a peer that only the SDK can reach (`LocalA2A`).
The model addresses both with the same `{"message": string}` argument shape
described in `docs/agent-runs-protocol.md` §4.2, so the same prompt works
unchanged whichever flavour is configured.

`LocalA2A` is **URL-only**: you supply the Agent Card URL (and optional
auth headers), and the SDK does the rest. On the first run / session the
SDK fetches the card with `net/http`, ships it inline with the agent
spec (so MANTYX never reaches your intranet directly), and on every
`local_tool_call` event with `kind: "a2a_local"` it speaks A2A's
JSON-RPC `message/send` against `agentCard.url`, returning the reply
text as the tool result. The fetched card is cached for the duration of
the run / session.

```go
result, err := client.RunAgent(ctx, mantyx.RunSpec{
    SystemPrompt: "You are a helpful router. Delegate billing to billing_agent.",
    Prompt:       "Why was I charged twice last month?",
    Tools: []mantyx.ToolRef{
        // Public peer MANTYX dials over A2A `message/send`.
        mantyx.MantyxA2A(mantyx.MantyxA2AOptions{
            Name:         "billing_agent",
            Description:  "Delegate billing questions to the Acme billing agent.",
            AgentCardURL: "https://billing.acme.com/.well-known/agent-card.json",
            Headers:      map[string]string{"Authorization": "Bearer " + os.Getenv("BILLING_TOKEN")},
        }),
        // Intranet peer the SDK can reach but MANTYX cannot.
        mantyx.LocalA2A(mantyx.LocalA2ASpec{
            Name:         "intranet_hr",
            AgentCardURL: "https://hr.intranet.acme/.well-known/agent-card.json",
            Headers:      map[string]string{"Authorization": "Bearer " + os.Getenv("HR_TOKEN")},
        }),
    },
})
```

> **Headers and secrets.** The `Headers` you pass are forwarded as-is —
> on the Agent Card GET (LocalA2A only) and on every `message/send` POST
> (both flavours). For long-lived credentials, register the peer as a
> workspace `ExternalAgent` instead; those headers support
> `{{secret:NAME}}` placeholders. Use the per-run header bag for
> short-lived, per-run tokens minted by your application.

### Exposing an agent over A2A

The inverse direction also works: wrap a MANTYX agent (ephemeral spec or a
persisted `AgentID`) and serve it as an Agent2Agent peer using the
[official A2A Go SDK](https://pkg.go.dev/github.com/a2aproject/a2a-go/v2)
mounted on `net/http`.

```go
import (
    mantyx "github.com/mantyx-io/mantyx-sdk/go"
    "github.com/mantyx-io/mantyx-sdk/go/a2asrv"
)

client := mantyx.NewClient(mantyx.Options{APIKey: "...", WorkspaceSlug: "acme"})

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

`github.com/a2aproject/a2a-go/v2` is pulled in as a regular dependency of
the `a2asrv` sub-package; consumers that don't import `a2asrv` don't pay
its cost in their final binary.

Each unique A2A `ContextID` opens a long-lived MANTYX session by default,
so multi-turn `SendMessage` calls share conversational history. Pass
`Conversation: a2asrv.ConversationStateless` to reduce every A2A request
to a one-shot `RunAgent` call. For lower-level integration (mounting the
executor in your own `net/http` mux), `a2asrv` also exports `NewExecutor`
which returns a value implementing the official `a2asrv.AgentExecutor`
interface.

## MCP connectors

Expose every tool published by an MCP server to the agent loop in one go,
without listing them individually.

`LocalMcp` is **URL-only** for HTTP and **command-only** for stdio. The
SDK uses the official
[`github.com/modelcontextprotocol/go-sdk/mcp`](https://pkg.go.dev/github.com/modelcontextprotocol/go-sdk/mcp)
package internally to open the transport, run `Initialize` + `tools/list`
on the first `RunAgent` / `Session.Send`, ship the resolved catalog
inline (with `<server>_<tool>` names) so MANTYX can render the tools to
the model, forward every `local_tool_call` event with `kind: "mcp_local"`
to the live MCP session via `tools/call`, and close the transport when
the run / session ends.

```go
result, err := client.RunAgent(ctx, mantyx.RunSpec{
    SystemPrompt: "You are a developer assistant with GitHub + filesystem access.",
    Prompt:       "Summarise the latest 5 issues on octocat/hello-world.",
    Tools: []mantyx.ToolRef{
        // Remote MCP server (Streamable HTTP) — MANTYX lists the catalog at
        // run start and proxies every call. Tools surface as `github_<tool>`.
        mantyx.MantyxMcp(mantyx.MantyxMcpOptions{
            Name:       "github",
            URL:        "https://mcp.github.com/v1",
            Headers:    map[string]string{"Authorization": "Bearer " + os.Getenv("GH_PAT")},
            ToolFilter: []string{"search_issues", "get_repo"},
        }),
        // Local Streamable HTTP MCP server — SDK manages discovery and tool calls.
        mantyx.LocalMcp(mantyx.LocalMcpSpec{
            Name:    "fs",
            URL:     "http://localhost:8080/mcp",
            Headers: map[string]string{"Authorization": "Bearer " + os.Getenv("FS_TOKEN")},
        }),
        // Or speak stdio to a local subprocess instead:
        // mantyx.LocalMcp(mantyx.LocalMcpSpec{
        //     Name:    "fs",
        //     Command: "mcp-server-filesystem",
        //     Args:    []string{"."},
        // }),
    },
})
```

If a remote (`kind: "mcp"`) MCP server is unreachable when the run starts,
MANTYX still exposes a single `<server>_unavailable` stub so the model can
tell the user why the connector is missing. Local MCP servers are
SDK-resolved end-to-end, so the SDK handles its own connection failures the
same way it would handle any other tool error — `RunAgent` returns it.

## Reasoning effort (`ReasoningLevel`)

Crank up provider thinking on reasoning models without writing
provider-specific code:

```go
_, err := client.RunAgent(ctx, mantyx.RunSpec{
    SystemPrompt:   "...",
    Prompt:         "Plan a multi-week migration.",
    ReasoningLevel: mantyx.ReasoningHigh(), // or mantyx.ReasoningEffort(80)
})
```

| Builder                  | Wire value | Notes |
| ------------------------ | ---------- | ----- |
| `mantyx.ReasoningOff()`     | `"off"`    | Disables provider thinking. |
| `mantyx.ReasoningLow()`     | `"low"`    | Web composer's "Fast" preset. |
| `mantyx.ReasoningMedium()`  | `"medium"` | Web composer's "Moderate" preset. |
| `mantyx.ReasoningHigh()`    | `"high"`   | Web composer's "Smart" preset. |
| `mantyx.ReasoningEffort(n)` | `n`        | Integer in `[0, 100]`. `0` disables thinking explicitly. |

The server maps this onto each LLM's native dial — `reasoning.effort` for
OpenAI, `thinkingConfig` for Gemini, extended-thinking budget for
Anthropic. Non-reasoning models silently ignore it. On sessions, pass
`mantyx.WithReasoningLevel(...)` to `Session.Send` to override the
session-wide value for one turn.

## Structured output (`OutputSchema`)

Constrain the assistant's **final reply** to a JSON document matching a
JSON Schema, and decode it into a Go struct with `mantyx.ParseRunOutput`.
`OutputSchema.Schema` accepts either a hand-rolled `map[string]any` /
`json.RawMessage` *or* a Go struct (or pointer-to-struct) — the SDK runs
the same struct-to-JSON-Schema reflection used by `LocalToolSpec.Parameters`
so you can drive both your typed receive shape and the schema you ship to
the provider from a single Go type:

```go
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
        log.Printf("model returned non-JSON: %q", pe.Text)
    }
    return err
}
```

If you'd rather keep the schema explicit, `OutputSchema.Schema` still
accepts a `map[string]any` or `json.RawMessage` containing the full JSON
Schema — both shapes are passed through verbatim.

The SDK validates `Name` (regex `^[a-zA-Z0-9_-]{1,64}$`), schema shape
(non-nil JSON object), and total size (≤ 32 KB) locally so you get a
typed `*mantyx.Error` up front instead of a server round-trip rejection.
On parse failure, `ParseRunOutput` returns `*mantyx.ParseError` with the
raw model text preserved on `Text`.

`OutputSchema` is independent of `ReasoningLevel` — combine the two for
deep-reasoning JSON outputs. On sessions it inherits from
`SessionSpec.OutputSchema` and can be overridden per turn via
`session.Send(ctx, prompt, mantyx.WithOutputSchema(...))`. See
`docs/wire-protocol.md` §7 for the full per-provider mapping.

### Structured output for local tools

`LocalToolSpec` accepts the same per-tool affordances as the wire
protocol: an `OutputSchema` describing the structured return value, and
a `LongRunning` flag that appends a "don't double-call while pending"
hint to the model-facing description. By default `OutputSchema` is
**inferred from `Execute`'s return type** using the same reflection
path `Parameters` already uses — so a single Go type can drive both
the schema you ship to the provider and the typed value you return:

```go
type KickOffArgs struct {
    Dataset string `json:"dataset"`
}
type KickOffResult struct {
    JobID  string `json:"jobId"  jsonschema:"Provider-side job id"`
    Status string `json:"status" jsonschema:"enum=pending,enum=done"`
}

mantyx.LocalTool(mantyx.LocalToolSpec{
    Name:        "kick_off_export",
    Description: "Start a long-running export job.",
    Parameters:  &KickOffArgs{},
    LongRunning: true,
    Execute: func(ctx context.Context, args KickOffArgs) (*KickOffResult, error) {
        return enqueueExport(ctx, args.Dataset)
    },
})
```

Inference is skipped when `Execute` returns `string` or
`json.RawMessage` (those are opaque text payloads) or when the
reflected schema's root is not a JSON object (providers reject
non-object roots in this position). To override the inferred schema —
or to attach one explicitly when `Execute` returns `string` — set
`OutputSchema` on the spec; it accepts the same shapes as `Parameters`
(`map[string]any`, `json.RawMessage`, or a struct / pointer-to-struct).

`LongRunning` is a pure annotation — MANTYX appends a stable hint to
the description and does *not* alter scheduling, the per-call timeout,
or the tool's lifecycle. See [`docs/tools/local`](https://docs.mantyx.com/docs/tools/local/)
for the full guide.

## Picking a model

```go
catalog, err := client.ListModels(ctx)
if err != nil {
    log.Fatal(err)
}
for _, m := range catalog.Models {
    fmt.Printf("%s\t%s\n", m.ID, m.Label)
}

result, err := client.RunAgent(ctx, mantyx.RunSpec{
    SystemPrompt: "...",
    Prompt:       "Hi!",
    ModelID:      "platform:cm6abc123",
})
```

`ModelID` accepts any of:

- `platform:<offeringId>` — a platform-hosted model offering.
- `provider:<llmProviderId>` — your own BYOK provider's default model.
- `provider:<llmProviderId>:<vendorModelId>` — your provider, override model.
- `<vendorModelId>` — bare vendor id; only resolves when one workspace
  provider can run it.
- empty — workspace default.

## Streaming tokens

```go
ch, err := client.StreamAgent(ctx, mantyx.RunSpec{
    SystemPrompt: "...",
    Prompt:       "Tell me a story.",
})
if err != nil {
    log.Fatal(err)
}
for ev := range ch {
    if ev.Type == "assistant_delta" {
        fmt.Print(ev.Text)
    }
}
fmt.Println()
```

## Multi-turn sessions

Sessions own the agent spec (system prompt, model, tool defs) and the full
message history. Each `Send` is a run scoped to the session.

```go
session, err := client.CreateSession(ctx, mantyx.SessionSpec{
    SystemPrompt: "You are a friendly REPL.",
    Tools: []mantyx.ToolRef{
        mantyx.LocalTool(mantyx.LocalToolSpec{
            Name:        "today",
            Description: "Get today's date as ISO 8601.",
            Parameters:  &struct{}{},
            Execute: func(ctx context.Context, _ struct{}) (string, error) {
                return time.Now().Format("2006-01-02"), nil
            },
        }),
    },
})

r1, _ := session.Send(ctx, "What day is it?")
fmt.Println(r1.Text)

r2, _ := session.Send(ctx, "And what about tomorrow?")
fmt.Println(r2.Text)

_ = session.End(ctx)
```

### Tagging runs and sessions with `Metadata`

Attach a flat `map[string]string` to runs and sessions so your team can filter
the dashboard by it (Agent runs → "Metadata" filter):

```go
// One-shot run
_, _ = client.RunAgent(ctx, mantyx.RunSpec{
    SystemPrompt: "...",
    Prompt:       "...",
    Metadata: map[string]string{
        "customer": "acme",
        "env":      "prod",
        "workflow": "support_triage",
    },
})

// Session — every run created via Session.Send inherits these tags
session, _ := client.CreateSession(ctx, mantyx.SessionSpec{
    SystemPrompt: "...",
    Metadata:     map[string]string{"customer": "acme", "env": "prod"},
})

// Per-message override; merged on top of the session's Metadata at run-creation
// time (run-level keys win).
_, _ = session.Send(ctx, "trace this turn",
    mantyx.WithMetadata(map[string]string{"trace_id": "trace_abc"}),
)
```

Limits enforced server-side: max 16 entries; keys match `[A-Za-z0-9._-]{1,64}`;
values are strings ≤ 256 chars; serialized JSON ≤ 4 KB. Bigger payloads return
`400 invalid_request`.

Resuming a session from a different process re-binds your local tool
handlers via `ResumeSession`:

```go
session, err := client.ResumeSession(ctx, sessionID, []mantyx.ToolRef{
    mantyx.LocalTool(mantyx.LocalToolSpec{ /* ... */ }),
})
```

## API reference

### Constructor

```go
type Options struct {
    APIKey        string
    WorkspaceSlug string
    BaseURL       string        // default: https://app.mantyx.io
    HTTPClient    *http.Client  // default: &http.Client{Timeout: 5 * time.Minute}
}

func NewClient(opts Options) *Client
```

### Methods

| Method                                                            | Returns                          |
| ----------------------------------------------------------------- | -------------------------------- |
| `(*Client).ListModels(ctx)`                                       | `(ModelCatalog, error)`          |
| `(*Client).RunAgent(ctx, RunSpec)`                                | `(*RunResult, error)`            |
| `(*Client).StreamAgent(ctx, RunSpec)`                             | `(<-chan RunEvent, error)`       |
| `(*Client).CreateSession(ctx, SessionSpec)`                       | `(*Session, error)`              |
| `(*Client).ResumeSession(ctx, id, tools)`                         | `(*Session, error)`              |
| `(*Session).Send(ctx, prompt, ...SendOption)`                     | `(*RunResult, error)`            |
| `(*Session).Stream(ctx, prompt)`                                  | `(<-chan RunEvent, error)`       |
| `(*Session).History(ctx)`                                         | `([]Message, error)`             |
| `(*Session).End(ctx)`                                             | `error`                          |
| `(*Client).CancelRun(ctx, runID)`                                 | `error`                          |

### Tools

| Helper                                              | Use case                                                                    |
| --------------------------------------------------- | --------------------------------------------------------------------------- |
| `LocalTool(LocalToolSpec)`                          | Define a local tool with Go-struct parameters and a handler.                |
| `LocalA2A(LocalA2ASpec)`                            | A2A peer addressed by `AgentCardURL`; SDK fetches the card and dials it.    |
| `LocalMcp(LocalMcpSpec)`                            | MCP server addressed by URL or stdio command; SDK manages it.               |
| `MantyxTool(id)`                                    | Reference an existing MANTYX tool by id.                                    |
| `MantyxPluginTool(name)`                            | Reference an installed platform plugin tool by name.                        |
| `MantyxA2A(MantyxA2AOptions)`                       | Remote Agent2Agent peer reachable from MANTYX (server-resolved).            |
| `MantyxMcp(MantyxMcpOptions)`                       | Remote MCP server (Streamable HTTP) MANTYX dials and proxies for you.       |

### Errors

The SDK returns typed errors that wrap `*mantyx.Error`:

- `*mantyx.AuthError` — 401/403 from the server.
- `*mantyx.NetworkError` — transport-layer failures.
- `*mantyx.RunError` — the agent loop terminated with an error.
- `*mantyx.ToolError` — a local tool handler returned an error or timed out.

Use `errors.As(err, &target)` to branch on type.

## Examples

Self-contained example projects live under [`examples/`](./examples/):

- `examples/oneshot` — minimal one-shot run with a local tool.
- `examples/session-chat` — interactive REPL on top of a session.
- `examples/mixed-tools` — combines local, MANTYX, and plugin tools.
- `examples/streaming` — token streaming to stdout.
- `examples/list-models` — model catalog + pick-and-run.
- `examples/a2a-tools` — remote (`MantyxA2A`) + local (`LocalA2A`) Agent2Agent peers.
- `examples/mcp-tools` — remote (`MantyxMcp`) + local (`LocalMcp`) MCP servers.

Each example has its own `go.mod`, with a `replace` directive pointing at
`../..` so it resolves the local SDK source. When you copy an example out of
the repo, remove that `replace` and run `go mod tidy`.

## Wire protocol

This SDK is a thin client over a stable HTTP/SSE protocol. The full
specification ships with the package at
[`docs/agent-runs-protocol.md`](./docs/agent-runs-protocol.md). Anyone can
implement a compatible client in another language.

## Development

```bash
go test ./...
go build ./...
```

The SDK has no MANTYX-internal Go modules in `go.mod`; only the standard
library, `github.com/google/jsonschema-go` (JSON Schema reflection for local
tool parameters), and `github.com/modelcontextprotocol/go-sdk` (drives the
Streamable HTTP and stdio transports for `LocalMcp`).

See [`CONTRIBUTING.md`](./CONTRIBUTING.md) for the contribution flow and
[`EXTRACT.md`](./EXTRACT.md) for the small steps to lift this folder into
its own public repository.

## License

[Apache-2.0](../LICENSE)
