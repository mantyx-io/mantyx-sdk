# Changelog

All notable changes to `mantyx-go-sdk` are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.0] — 2026-05-02

### Added

- Initial release of `mantyx-go-sdk`.
- `RunSpec.AgentID` / `SessionSpec.AgentID` — trigger a persisted MANTYX
  agent by id instead of defining an ephemeral one inline. The server
  hydrates the system prompt, model, and the agent's own tools (memory,
  skills, plugin tools, …) from the `Agent` row at run time. Any extra
  `Tools` you pass on the request are merged on top — typically `LocalTool`
  refs the agent should call back into for that run. `SystemPrompt` is
  now optional whenever `AgentID` is set.
- `Client` for the public agent-runs HTTP/SSE protocol.
- `RunAgent`, `StreamAgent` for one-shot ephemeral agent runs.
- `CreateSession`, `ResumeSession` and `Session.Send` / `Session.Stream` /
  `Session.History` / `Session.End` for multi-turn sessions.
- `ListModels` for the workspace's model catalog (BYOK + platform offerings).
- `LocalTool`, `MantyxTool`, `MantyxPluginTool` tool helpers.
- Struct → JSON Schema conversion for local tool parameters via
  `github.com/invopop/jsonschema`.
- SSE consumer with `Last-Event-ID` reconnect support.
- Typed error hierarchy: `Error`, `AuthError`, `NetworkError`, `RunError`,
  `ToolError`.
- Self-contained example projects under `examples/`, each with its own
  `go.mod`.
- `httptest`-driven tests for runs, sessions, and the model catalog.

[Unreleased]: https://github.com/mantyx/aos/compare/sdk-go-v0.1.0...HEAD
[0.1.0]: https://github.com/mantyx/aos/releases/tag/sdk-go-v0.1.0
