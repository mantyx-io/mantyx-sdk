# mantyx-go-sdk

The official Go SDK for the [MANTYX](https://mantyx.com) agent runtime.
Define ephemeral agents that mix server-side MANTYX tools with
locally-executed tools, run them remotely, and stream events back into your
program.

- LLM loop runs on MANTYX (BYOK or platform-hosted models).
- Server-side tools (`mantyx`, `mantyx_plugin`) execute inside MANTYX.
- Local tools execute in *your* process; the SDK shuttles arguments and
  results over an SSE stream + a tool-result POST.
- One-shot runs and multi-turn sessions, both with persisted observability.
- Authenticated with a single workspace API key.

For background, see the [agent-runs protocol spec](./docs/agent-runs-protocol.md).

## Install

```bash
go get github.com/mantyx/mantyx-go-sdk@latest
```

Requires Go 1.22+. The only third-party dependency is
[`github.com/invopop/jsonschema`](https://github.com/invopop/jsonschema)
(MIT) for converting Go structs into JSON Schema documents for local tool
parameters.

## Quickstart

```go
package main

import (
    "context"
    "encoding/json"
    "fmt"
    "log"
    "os"

    mantyx "github.com/mantyx/mantyx-go-sdk"
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
                Execute: func(ctx context.Context, raw json.RawMessage) (string, error) {
                    var args readFileArgs
                    if err := json.Unmarshal(raw, &args); err != nil {
                        return "", err
                    }
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
            Execute: func(ctx context.Context, raw json.RawMessage) (string, error) {
                var args struct{ Path string `json:"path"` }
                if err := json.Unmarshal(raw, &args); err != nil { return "", err }
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
            Execute: func(ctx context.Context, _ json.RawMessage) (string, error) {
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
    BaseURL       string        // default: https://api.mantyx.com
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

| Helper                                              | Use case                                                         |
| --------------------------------------------------- | ---------------------------------------------------------------- |
| `LocalTool(LocalToolSpec)`                          | Define a local tool with Go-struct parameters and a handler.     |
| `MantyxTool(id)`                                    | Reference an existing MANTYX tool by id.                         |
| `MantyxPluginTool(name)`                            | Reference an installed platform plugin tool by name.             |

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
library and `github.com/invopop/jsonschema`.

See [`CONTRIBUTING.md`](./CONTRIBUTING.md) for the contribution flow and
[`EXTRACT.md`](./EXTRACT.md) for the small steps to lift this folder into
its own public repository.

## License

[Apache-2.0](./LICENSE)
