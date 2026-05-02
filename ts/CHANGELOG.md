# Changelog

All notable changes to `@mantyx/sdk` are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.0] — 2026-05-02

### Added

- Initial release of `@mantyx/sdk`.
- `RunSpec.agentId` / `SessionSpec.agentId` — trigger a persisted MANTYX
  agent by id instead of defining an ephemeral one inline. The server
  hydrates the system prompt, model, and the agent's own tools (memory,
  skills, plugin tools, …) from the `Agent` row at run time. Any extra
  `tools` you pass on the request are merged on top — typically local
  tools the agent should call back into for that run. `systemPrompt` is
  now optional whenever `agentId` is set.
- `MantyxClient` for the public agent-runs HTTP/SSE protocol.
- `runAgent`, `streamAgent` for one-shot ephemeral agent runs.
- `createSession`, `resumeSession`, `endSession` for multi-turn sessions.
- `listModels` for the workspace's model catalog (BYOK + platform offerings).
- `defineLocalTool`, `mantyxTool`, `mantyxPluginTool` tool helpers.
- Zod → JSON Schema conversion for local tool parameters.
- Inline SSE parser (no extra deps) with `Last-Event-ID` reconnect support.
- Custom error hierarchy: `MantyxError`, `MantyxAuthError`,
  `MantyxNetworkError`, `MantyxRunError`, `MantyxToolError`.
- Self-contained example projects under `examples/`.
- Vitest-driven mock server tests for runs, sessions, and the model catalog.

[Unreleased]: https://github.com/mantyx/aos/compare/sdk-typescript-v0.1.0...HEAD
[0.1.0]: https://github.com/mantyx/aos/releases/tag/sdk-typescript-v0.1.0
