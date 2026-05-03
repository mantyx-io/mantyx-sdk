# Changelog

All notable changes to `mantyx-sdk` (PyPI distribution) are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

From release `0.2.0` onwards this file is regenerated from the
[Conventional Commits](https://www.conventionalcommits.org/) history by
`scripts/changelog.mjs --write` (see [`CONTRIBUTING.md`](../CONTRIBUTING.md)).

## [Unreleased]

## [0.1.0] — 2026-05-03

### Added

- Initial release of `mantyx-sdk` on PyPI; `import mantyx`.
- Synchronous `MantyxClient` and asynchronous `AsyncMantyxClient`,
  both backed by [`httpx`](https://www.python-httpx.org/).
- `run_agent`, `stream_agent` for one-shot ephemeral agent runs.
- `create_session`, `resume_session`, `end_session` for multi-turn
  sessions plus a per-message metadata override.
- `agent_id` on every entry point — trigger a persisted MANTYX agent by
  id and merge `local` tools on top of its stored configuration.
- `list_models` for the workspace's model catalog (BYOK + platform).
- `define_local_tool`, `mantyx_tool`, `mantyx_plugin_tool` helpers.
- Pydantic v2 → JSON Schema conversion for local tool parameters.
- Inline SSE parser (no extra deps) with `Last-Event-ID` reconnect
  support; tool-result POSTs run on a thread pool / asyncio task.
- Error hierarchy: `MantyxError`, `MantyxAuthError`,
  `MantyxNetworkError`, `MantyxRunError`, `MantyxToolError`.
- Self-contained example projects under `examples/`
  (`oneshot-local-tool`, `session-chat`, `mixed-tools`, `streaming`,
  `list-models`, `agent-by-id`).
- Pytest mock-server suite covering runs, sessions, models, errors,
  version sync, and the async client.
- PEP 561 `py.typed` marker and strict mypy compatibility.

[Unreleased]: https://github.com/mantyx-io/mantyx-sdk/compare/python/v0.1.0...HEAD
[0.1.0]: https://github.com/mantyx-io/mantyx-sdk/releases/tag/python/v0.1.0
