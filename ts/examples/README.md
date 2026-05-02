# `@mantyx/sdk` examples

Each subdirectory is a **self-contained project** with its own `package.json`,
`tsconfig.json`, and `README.md`. You can copy any one of them out of the
monorepo and run it as a standalone npm project — the only edits required are
documented in each `README.md`.

| Folder                | What it shows                                                              |
| --------------------- | -------------------------------------------------------------------------- |
| `oneshot-local-tool/` | A one-shot run with one local tool (`read_file`).                          |
| `agent-by-id/`        | Trigger a persisted MANTYX agent by `agentId` and merge in a local tool.   |
| `session-chat/`       | Multi-turn `AgentSession` driving an interactive REPL.                     |
| `mixed-tools/`        | Combine `mantyxTool`, `mantyxPluginTool`, and a local tool in one agent.   |
| `streaming/`          | Use `client.streamAgent()` to print assistant deltas to stdout.            |
| `list-models/`        | Call `client.listModels()`, pretty-print, then run an agent on the first. |

All examples read configuration from environment variables:

```bash
export MANTYX_API_KEY="mk_..."
export MANTYX_WORKSPACE_SLUG="acme-corp"
# Optional:
export MANTYX_BASE_URL="https://api.mantyx.com"     # self-hosted? override here
```

To run from inside this repo:

```bash
cd packages/mantyx-sdk/ts/examples/oneshot-local-tool
pnpm install
pnpm start
```

To run after copying an example out of this repo:

```bash
cd ./oneshot-local-tool
# Edit package.json: replace "@mantyx/sdk": "workspace:*" with the published version.
npm install
npm start
```
