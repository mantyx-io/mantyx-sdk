# `mantyx-go-sdk` examples

Each subdirectory is its own Go module with a `replace` directive that points
back at this repo so it builds in-tree:

```
require github.com/mantyx-io/mantyx-go-sdk v0.0.0
replace github.com/mantyx-io/mantyx-go-sdk => ../..
```

To run any example after publishing the SDK as a real Go module, delete the
`replace` line in `go.mod` and run `go get github.com/mantyx-io/mantyx-go-sdk@latest`.

| Folder           | What it shows                                                              |
| ---------------- | -------------------------------------------------------------------------- |
| `oneshot/`       | A one-shot run with one local tool.                                        |
| `agent-by-id/`   | Trigger a persisted MANTYX agent by `AgentID` and merge in a `LocalTool`.  |
| `session-chat/`  | Multi-turn `Session` driving an interactive REPL.                          |
| `mixed-tools/`   | Combine `MantyxTool`, `MantyxPluginTool`, and a local tool in one agent.   |
| `streaming/`     | Use `Client.StreamAgent()` and select on the event channel.                |
| `list-models/`   | Call `Client.ListModels()`, pretty-print, then run an agent on the first. |

All examples use `MANTYX_API_KEY` and `MANTYX_WORKSPACE_SLUG` env vars.
