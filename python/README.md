# mantyx-sdk

The official Python SDK for the [MANTYX](https://mantyx.com) agent runtime. Define ephemeral agents that mix server-side MANTYX tools with locally-executed tools, run them remotely, and stream events back into your process.

- LLM loop runs on MANTYX (BYOK or platform-hosted models).
- Server-side tools (`mantyx`, `mantyx_plugin`) execute inside MANTYX.
- Local tools execute inside *your* Python process; the SDK shuttles inputs and outputs over an SSE stream + a tool-result POST.
- Sync **and** async clients (`MantyxClient`, `AsyncMantyxClient`), both backed by [`httpx`](https://www.python-httpx.org/).
- One-shot runs and multi-turn sessions, both with persisted observability.
- Authenticated with a single workspace API key.

For background, see the [agent-runs protocol spec](./docs/agent-runs-protocol.md) (a copy ships with the package).

## Install

```bash
pip install mantyx-sdk
# or: uv add mantyx-sdk
# or: poetry add mantyx-sdk
```

Requires Python 3.9+ and runs on macOS, Linux, and Windows. The runtime dependencies are [`httpx`](https://www.python-httpx.org/) and [`pydantic`](https://docs.pydantic.dev/) v2.

## Quickstart

```python
import os
from pathlib import Path

from pydantic import BaseModel
from mantyx import MantyxClient, define_local_tool, mantyx_tool


class ReadFileArgs(BaseModel):
    path: str


client = MantyxClient(
    api_key=os.environ["MANTYX_API_KEY"],
    workspace_slug=os.environ["MANTYX_WORKSPACE_SLUG"],
    # base_url="https://app.mantyx.io",  # override for self-hosted
)

result = client.run_agent(
    system_prompt="You are a helpful assistant.",
    prompt="Read /etc/hostname and summarise what it says.",
    tools=[
        # Local tool — defined and executed in this process.
        define_local_tool(
            name="read_file",
            description="Read a file from the local filesystem.",
            parameters=ReadFileArgs,
            execute=lambda args: Path(args.path).read_text(),
        ),
        # Reference to an existing MANTYX workspace tool.
        mantyx_tool("tool_cm6abc123"),
    ],
)

print(result.text)
```

The SDK opens an SSE stream to MANTYX, listens for `local_tool_call` events, runs the matching local handler, and POSTs the result back. The server keeps running the agent loop until it produces a final reply.

## Async client

```python
import asyncio

from mantyx import AsyncMantyxClient


async def main() -> None:
    async with AsyncMantyxClient(
        api_key="...",
        workspace_slug="acme-corp",
    ) as client:
        result = await client.run_agent(
            system_prompt="You are a helpful assistant.",
            prompt="Hi!",
        )
        print(result.text)


asyncio.run(main())
```

`AsyncMantyxClient` exposes the same API as `MantyxClient` with `async`/`await` semantics. Local tool handlers may be sync **or** async — the SDK awaits them as needed.

## Triggering a persisted MANTYX agent

Pass `agent_id` to run an agent that already exists in your workspace. The server hydrates the agent's system prompt, model, and server-side tools (memory, skills, plugin tools, …) from the `Agent` row at run time. Anything you pass in `tools` is **merged on top** — typically `local` tools you want the agent to be able to call back into for this specific run.

```python
from pathlib import Path

from pydantic import BaseModel
from mantyx import MantyxClient, define_local_tool


class ReadFileArgs(BaseModel):
    path: str


client = MantyxClient(api_key="...", workspace_slug="acme")

result = client.run_agent(
    agent_id="agent_cm6abc123",  # workspace agent id
    prompt="Pull the latest deploy logs and summarise them.",
    tools=[
        define_local_tool(
            name="read_local_file",
            parameters=ReadFileArgs,
            execute=lambda args: Path(args.path).read_text(),
        ),
    ],
)
print(result.text)
```

Notes:

- `system_prompt` becomes optional when `agent_id` is set; if both are sent, the agent's stored prompt wins.
- `model_id` is also optional: omit it to use the agent's configured LLM provider, or pass it to override the model for this run.
- The API key must be authorized for the agent (an empty `agentIds` allowlist on the key counts as "all agents in the workspace"). Otherwise the call returns `403`.

The same `agent_id` field works on `client.create_session(...)` for multi-turn conversations against a persisted agent.

## Picking a model

```python
catalog = client.list_models()
print("\n".join(f"{m.id}\t{m.label}" for m in catalog.models))

client.run_agent(
    system_prompt="...",
    prompt="Hi!",
    model_id="platform:cm6abc123",  # or "provider:<id>", or "<vendorModelId>"
)
```

`model_id` accepts:

- `platform:<offeringId>` — a platform-hosted model offering.
- `provider:<llmProviderId>` — your own BYOK provider's default model.
- `provider:<llmProviderId>:<vendorModelId>` — your provider, override model.
- `<vendorModelId>` — bare vendor id; only resolves when one workspace provider can run it.
- omitted — workspace default.

## Streaming tokens

```python
# Iterator
for event in client.stream_agent(system_prompt="...", prompt="Tell me a story."):
    if event.type == "assistant_delta":
        print(event.text, end="", flush=True)
    elif event.type == "result":
        print()

# Or use the on_assistant_delta callback on run_agent:
client.run_agent(
    system_prompt="...",
    prompt="...",
    on_assistant_delta=lambda d: print(d, end="", flush=True),
)

# Async equivalents
async for event in async_client.stream_agent(system_prompt="...", prompt="..."):
    ...
```

## Multi-turn sessions

Sessions own the agent spec (system prompt, model, tool defs) and the full message history. Each `send` is a run scoped to the session.

```python
from datetime import date

from pydantic import BaseModel
from mantyx import MantyxClient, define_local_tool


class TodayArgs(BaseModel):
    pass


client = MantyxClient(api_key="...", workspace_slug="acme")

session = client.create_session(
    system_prompt="You are a friendly REPL.",
    tools=[
        define_local_tool(
            name="today",
            description="Get today's date as ISO 8601.",
            parameters=TodayArgs,
            execute=lambda _args: date.today().isoformat(),
        ),
    ],
)

r1 = session.send("What day is it?")
print(r1.text)

r2 = session.send("And what about tomorrow?")
print(r2.text)

session.end()
```

Resuming a session from a different process re-binds your local tool handlers; pass them in via `resume_session`:

```python
session = client.resume_session(
    session_id,
    tools=[
        define_local_tool(name="today", parameters=TodayArgs, execute=...),
    ],
)
```

### Tagging runs and sessions with `metadata`

Attach a flat `dict[str, str]` to runs and sessions so your team can filter the dashboard by it:

```python
client.run_agent(
    system_prompt="...",
    prompt="...",
    metadata={"customer": "acme", "env": "prod", "workflow": "support_triage"},
)

session = client.create_session(
    system_prompt="...",
    metadata={"customer": "acme", "env": "prod"},
)

# Per-message override — merged on top of the session's metadata
# (run-level keys win)
session.send("trace this turn", metadata={"trace_id": "trace_abc"})
```

Limits enforced server-side: max 16 entries; keys match `[A-Za-z0-9._-]{1,64}`; values are strings ≤ 256 chars; serialised JSON ≤ 4 KB. Bigger payloads return `400 invalid_request`.

## API reference

### `MantyxClient(...)` / `AsyncMantyxClient(...)`

```python
class MantyxClient:
    def __init__(
        self,
        *,
        api_key: str,
        workspace_slug: str,
        base_url: str = "https://app.mantyx.io",
        timeout: float = 60.0,
        http_client: httpx.Client | None = None,
    ): ...
```

`AsyncMantyxClient` accepts an `httpx.AsyncClient` instead.

### Methods

| Method                                          | Returns (sync)                       |
| ----------------------------------------------- | ------------------------------------ |
| `list_models()`                                 | `ModelCatalog`                       |
| `run_agent(...)`                                | `RunResult`                          |
| `stream_agent(...)`                             | `Iterator[RunEvent]`                 |
| `create_session(...)`                           | `AgentSession`                       |
| `resume_session(session_id, *, tools=None)`     | `AgentSession`                       |
| `end_session(session_id)`                       | `None`                               |
| `cancel_run(run_id)`                            | `None`                               |

The async client returns awaitable / async-iterator equivalents (e.g. `await async_client.run_agent(...)`, `async for ev in async_client.stream_agent(...)`).

### Tools

| Helper                       | Use case                                                              |
| ---------------------------- | --------------------------------------------------------------------- |
| `define_local_tool(...)`     | Define a local tool with a Pydantic parameter schema and handler.     |
| `mantyx_tool(id)`            | Reference an existing MANTYX workspace tool by id.                    |
| `mantyx_plugin_tool(name)`   | Reference an installed platform plugin tool by `@plugin/tool` name.   |

### Errors

All raised errors extend `MantyxError`. Common subclasses:

- `MantyxAuthError` — 401/403 from the server (bad API key, wrong workspace).
- `MantyxNetworkError` — transport-layer failures.
- `MantyxRunError` — the agent loop terminated with an error.
- `MantyxToolError` — a local tool handler raised or timed out.

## Examples

Self-contained example projects live under [`examples/`](./examples/):

- `examples/oneshot-local-tool` — minimal one-shot run with a local tool.
- `examples/session-chat` — interactive REPL on top of a session.
- `examples/mixed-tools` — combines local, MANTYX, and plugin tools.
- `examples/streaming` — token streaming to stdout.
- `examples/list-models` — model catalog + pick-and-run.
- `examples/agent-by-id` — trigger a persisted MANTYX agent by id.

Each example is its own project (`pyproject.toml`, `README.md`, `main.py`) so you can copy any one of them out of the repo and run it standalone.

## Wire protocol

This SDK is a thin client over a stable HTTP/SSE protocol. The full specification ships with the package at [`docs/agent-runs-protocol.md`](./docs/agent-runs-protocol.md). Anyone can implement a compatible client in another language.

## Development

```bash
python -m venv .venv
. .venv/bin/activate
python -m pip install -e ".[dev]"

pytest -q          # unit + mock-server tests
ruff check .       # lint
ruff format .      # format
mypy src           # strict type-check
```

The SDK has no internal `workspace:*`-style dependencies. `python -m build` produces a self-contained `dist/` ready for `python -m twine upload` (or PyPI Trusted Publishing — see [`CONTRIBUTING.md`](./CONTRIBUTING.md)).

See [`CONTRIBUTING.md`](./CONTRIBUTING.md) for the contribution flow and [`EXTRACT.md`](./EXTRACT.md) for the (very small) steps to lift this folder into its own public repository.

## License

[Apache-2.0](../LICENSE)
