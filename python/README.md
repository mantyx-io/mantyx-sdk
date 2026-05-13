# mantyx-sdk

The official Python SDK for the [MANTYX](https://mantyx.com) agent runtime. Define ephemeral agents that mix server-side MANTYX tools with locally-executed tools, run them remotely, and stream events back into your process.

- LLM loop runs on MANTYX (BYOK or platform-hosted models).
- Server-side tools (`mantyx`, `mantyx_plugin`, `mantyx_a2a`, `mantyx_mcp`) execute inside MANTYX.
- Client-resolved tools (`define_local_tool`, `define_local_a2a`, `define_local_mcp`) execute inside *your* Python process; the SDK shuttles inputs and outputs over an SSE stream + a tool-result POST.
- Tune the LLM's thinking budget per run with `reasoning_level` (`"off" | "low" | "medium" | "high"` or an int 0..100).
- Sync **and** async clients (`MantyxClient`, `AsyncMantyxClient`), both backed by [`httpx`](https://www.python-httpx.org/).
- One-shot runs and multi-turn sessions, both with persisted observability.
- Authenticated with a single bearer credential — either a workspace API
  key (token prefix `mantyx_`) or a MANTYX OAuth 2.0 access token
  (`mantyx_at_`). Both flow through the same `Authorization: Bearer …`
  header and are interchangeable end-to-end.

For background, see the [agent-runs protocol spec](./docs/agent-runs-protocol.md) (a copy ships with the package).

## Install

```bash
pip install mantyx-sdk
# or: uv add mantyx-sdk
# or: poetry add mantyx-sdk
```

Requires Python 3.10+ and runs on macOS, Linux, and Windows. The runtime dependencies are [`httpx`](https://www.python-httpx.org/), [`pydantic`](https://docs.pydantic.dev/) v2, the official [`mcp`](https://pypi.org/project/mcp/) package (used internally by `define_local_mcp`), and [`anyio`](https://anyio.readthedocs.io/) (sync/async bridge for the MCP client).

## Quickstart

```python
import os
from pathlib import Path

from pydantic import BaseModel
from mantyx import MantyxClient, define_local_tool, mantyx_tool


class ReadFileArgs(BaseModel):
    path: str


client = MantyxClient(
    # Use *either* `api_key` (workspace API key, prefix `mantyx_`) or
    # `access_token` (OAuth 2.0 access token, prefix `mantyx_at_`). The
    # server resolves either by token-prefix; the SDK only ever ships
    # one `Authorization: Bearer …` header.
    api_key=os.environ["MANTYX_API_KEY"],
    # access_token=os.environ["MANTYX_ACCESS_TOKEN"],
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
        api_key: str | None = None,
        access_token: str | None = None,
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
| `define_local_a2a(...)`      | Register an A2A peer by `agent_card_url`; the SDK fetches and dials it. |
| `define_local_mcp(...)`      | Declare an MCP server by URL or stdio command; the SDK manages it.    |
| `mantyx_tool(id)`            | Reference an existing MANTYX workspace tool by id.                    |
| `mantyx_plugin_tool(name)`   | Reference an installed platform plugin tool by `@plugin/tool` name.   |
| `mantyx_a2a(...)`            | Reference a remote A2A peer MANTYX dials directly.                    |
| `mantyx_mcp(...)`            | Reference a remote MCP server (Streamable HTTP) MANTYX proxies.       |

#### Agent2Agent delegation

Two flavours, addressed identically by the model with `{ "message": str }`:

```python
from mantyx import define_local_a2a, mantyx_a2a

remote_billing = mantyx_a2a(
    name="billing_agent",
    agent_card_url="https://billing.example.com/.well-known/agent-card.json",
    description="Delegate billing questions to the public Acme billing agent.",
    headers={"Authorization": "Bearer ..."},
)

local_hr = define_local_a2a(
    name="intranet_hr_agent",
    agent_card_url="https://hr.intranet.acme/.well-known/agent-card.json",
    headers={"Authorization": "Bearer ..."},  # optional
)
```

`mantyx_a2a` is server-resolved: MANTYX dials `agent_card_url` over A2A's
`message/send` RPC and forwards the reply as the tool result.

`define_local_a2a` is client-resolved but **URL-only**: you pass the
`agent_card_url` (and optional `headers`), and the SDK takes care of the
rest. On the first run / session, the SDK fetches the Agent Card with
`httpx`, ships it inline with the agent spec (so MANTYX never reaches
your intranet), and on every `local_tool_call` event with
`kind: "a2a_local"` it speaks A2A's JSON-RPC `message/send` against
`agent_card.url`, returning the reply text as the tool result. The
fetched card is cached for the duration of the run / session.

##### Exposing an agent over A2A

The inverse direction also works: wrap a MANTYX agent (ephemeral spec or a
persisted `agent_id`) and serve it as an Agent2Agent peer using the official
[`a2a-sdk`](https://pypi.org/project/a2a-sdk/) Python package mounted on
Starlette + uvicorn.

```python
import asyncio
from mantyx import AsyncMantyxClient
from mantyx.a2a_server import build_agent_card, serve_agent_over_a2a

async def main() -> None:
    async with AsyncMantyxClient(api_key="...", workspace_slug="acme") as client:
        handle = await serve_agent_over_a2a(
            client=client,
            agent_card=build_agent_card(
                name="Acme Support",
                description="Customer support questions.",
                version="1.0.0",
                public_url="http://localhost:4000",
            ),
            agent_id="agent_cm6abc123",  # or system_prompt=..., model_id=..., tools=[...]
            port=4000,
        )
        print(f"A2A peer up on {handle.url}")
        await handle.serve_forever()

asyncio.run(main())
```

`a2a-sdk[http-server]` and `uvicorn` ship as the **`[a2a-server]` extra**
so the base wheel stays slim:

```bash
pip install "mantyx-sdk[a2a-server]"
```

Each unique A2A `context_id` opens a long-lived MANTYX session by default,
so multi-turn `message/send` calls share conversational history. Pass
`conversation="stateless"` to reduce every A2A request to a one-shot
`run_agent` call. For lower-level integration (mounting the executor in
your own Starlette / FastAPI app) `mantyx.a2a_server` also exports a
`MantyxAgentExecutor` class implementing `a2a.server.agent_execution.AgentExecutor`.

#### MCP connectors

Tools surface to the model as `<server>_<tool>` regardless of flavour:

```python
from mantyx import define_local_mcp, mantyx_mcp


remote_github = mantyx_mcp(
    name="github",
    url="https://api.example.com/mcp",
    headers={"Authorization": "Bearer ..."},
    tool_filter=["create_issue", "list_issues"],  # optional allow-list
)

# Streamable HTTP transport
local_fs_http = define_local_mcp(
    name="fs",
    url="http://localhost:8080/mcp",
    headers={"Authorization": "Bearer ..."},  # optional
)

# stdio transport
local_fs_stdio = define_local_mcp(
    name="fs",
    command="mcp-server-filesystem",
    args=["."],
    env={"FOO": "bar"},  # optional
    cwd="/workspace",     # optional
)
```

`mantyx_mcp` is server-resolved: MANTYX speaks Streamable HTTP MCP to the
upstream, lists its catalog, and proxies tool calls — prefixing every
discovered tool name as `<server>_<tool>`.

`define_local_mcp` is client-resolved but **URL-only** (or stdio
`command`-only). You point at the server and the SDK does the rest using
the official [`mcp`](https://pypi.org/project/mcp/) package: it opens the
transport, runs `Initialize` + `tools/list` on the first `run_agent` /
`session.send`, ships the resolved catalog (with `<server>_<tool>` names)
inline so MANTYX can render the tools to the model, forwards every
`local_tool_call` event with `kind: "mcp_local"` to the live MCP session
via `tools/call`, and closes the transport when the run / session ends
(`session.end()` for sessions, automatically for one-shot runs). Sync
clients drive the async MCP SDK transparently via an `anyio.BlockingPortal`.

#### Reasoning effort (`reasoning_level`)

Pass `reasoning_level` on `run_agent` / `stream_agent` / `create_session`
(and per-message via `session.send(prompt, reasoning_level=...)`) to dial
provider thinking. Accepts a string anchor (`"off"`, `"low"`, `"medium"`,
`"high"`) or an integer in `[0, 100]`. The SDK forwards the value as-is;
MANTYX maps it onto each LLM's native dial — see
`docs/agent-runs-protocol.md` §4.4.

```python
client.run_agent(system_prompt="...", prompt="...", reasoning_level="medium")
client.run_agent(system_prompt="...", prompt="...", reasoning_level=80)
```

#### Structured output (`output_schema`)

Constrain the assistant's **final reply** to a JSON document matching a
JSON Schema, and decode it with a Pydantic (or any) validator via
`parse_run_output`:

```python
from pydantic import BaseModel
from mantyx import MantyxClient, parse_run_output

class Weather(BaseModel):
    city: str
    temperature_c: float

WEATHER_SCHEMA = {
    "type": "object",
    "properties": {
        "city":          {"type": "string"},
        "temperature_c": {"type": "number"},
    },
    "required": ["city", "temperature_c"],
    "additionalProperties": False,
}

result = client.run_agent(
    system_prompt="Return the weather as JSON.",
    prompt="What's the weather in San Francisco right now?",
    output_schema={"name": "weather_report", "schema": WEATHER_SCHEMA},
)
report = parse_run_output(result, Weather.model_validate)
# report.city / report.temperature_c are typed.
```

`output_schema` validates the `name` regex (`^[a-zA-Z0-9_-]{1,64}$`),
schema shape, and serialised size (≤ 32 KB) locally so you get a typed
`ValueError` up front. On rare parse failures `parse_run_output` raises
`MantyxParseError` with the raw text preserved on the `text` attribute.
Available on both sync and async clients, on `run_agent` /
`stream_agent` / `create_session`, and as a per-message override on
`session.send` / `session.stream`. See `docs/wire-protocol.md` §7 for
the per-provider mapping.

#### Structured output for local tools

`define_local_tool` accepts the same per-tool affordances as the wire
protocol: an `output_schema` (Pydantic model or JSON Schema dict)
describing the tool's structured return value, and a `long_running`
flag that appends a "don't double-call while pending" hint to the
model-facing description.

```python
from pydantic import BaseModel
from mantyx import define_local_tool

class KickOffArgs(BaseModel):
    dataset: str

class KickOffResult(BaseModel):
    job_id: str
    status: str  # "pending" | "done"

define_local_tool(
    name="kick_off_export",
    description="Start a long-running export job.",
    parameters=KickOffArgs,
    output_schema=KickOffResult,
    long_running=True,
    execute=lambda args: enqueue_export(args.dataset),
)
```

`output_schema` is forwarded to providers with per-tool response
schemas (Gemini's `responseJsonSchema` on the FunctionDeclaration);
other engines surface it via the description. `long_running` is a pure
annotation — MANTYX appends a stable hint and does *not* alter
scheduling or timeouts. See [`docs/tools/local`](https://docs.mantyx.com/docs/tools/local/)
for the full guide.

### Errors

All raised errors extend `MantyxError`. Common subclasses:

- `MantyxAuthError` — 401 from the server (bad / missing API key or
  OAuth access token).
- `MantyxScopeError` — 403 `insufficient_scope` from the server. The
  OAuth access token is missing one of the scopes a route demands;
  `err.required_scopes` lists them so callers can drive a re-consent
  flow. API keys never trip this — it is OAuth-only.
- `MantyxOAuthError` — non-2xx from the OAuth token / revoke endpoint.
  Carries the RFC 6749 `oauth_error` (`"invalid_grant"`, …) and the
  optional `oauth_error_description`. `invalid_grant` on refresh means
  the refresh token was revoked — route the user back to first sign-in.
- `MantyxNetworkError` — transport-layer failures.
- `MantyxRunError` — the agent loop terminated with an error.
- `MantyxToolError` — a local tool handler raised or timed out.
- `MantyxParseError` — `parse_run_output` failed to JSON-decode the run's
  terminal text (or the user-supplied validator rejected it).

### OAuth 2.0 refresh

The SDK ships a **refresh-only** OAuth client. It assumes the calling
app already obtained a refresh token through its own sign-in flow
(browser PKCE redirect, native auth, server-side exchange — whatever
fits the host). The library does not drive consent and does not
initiate authorization-code or client-credentials grants. Once you
have the refresh token, hand it to the SDK and the rest is
transparent:

- Refresh tokens are **persistent and non-rotating** per
  [`docs/oauth.md`](./docs/oauth.md): store them once at first
  sign-in (treat them as long-lived, encrypted at rest) and the SDK
  re-mints access tokens from the same value on demand.
- A `TokenSource` is called before every request and again on `401`,
  with single-flight collapse on concurrent refreshes.
- `400 invalid_grant` from the token endpoint surfaces as
  `MantyxOAuthError` — that means the refresh has been revoked and
  the caller has to drive a fresh sign-in.

```python
from mantyx import MantyxClient, MantyxOAuthClient

oauth = MantyxOAuthClient(
    client_id=os.environ["MANTYX_OAUTH_CLIENT_ID"],         # mantyx_oa_…
    client_secret=os.environ["MANTYX_OAUTH_CLIENT_SECRET"], # mantyx_oas_…
)

# (1) Hand the SDK a stored refresh token — it caches the access token in
#     memory, refreshes proactively before expiry, and retries the original
#     request once on a 401.
client = MantyxClient(
    token_source=oauth.refresh_token_source(
        refresh_token=stored_refresh_token,                  # mantyx_rt_…
    ),
    workspace_slug="acme",
)

# (2) If the calling app already has a non-expired access token in hand
#     (e.g. straight out of its sign-in flow), pass it as `initial_token` to
#     skip the first /token round-trip.
seeded = MantyxClient(
    token_source=oauth.refresh_token_source(
        refresh_token=stored_refresh_token,
        initial_token=token_from_sign_in,
    ),
    workspace_slug="acme",
)

# (3) Manual override for short-lived access tokens the caller manages
#     itself — no refresh, no retry, no OAuth client needed.
one_shot = MantyxClient(access_token="mantyx_at_…", workspace_slug="acme")

# Optional: revoke a refresh token at sign-out — this kills the refresh and
# every live access token tied to its grant.
oauth.revoke(token=stored_refresh_token)
```

For the async client use `oauth.async_refresh_token_source(...)`
(and `await oauth.arefresh(...)` / `await oauth.arevoke(...)` for
ad-hoc calls). See [`docs/oauth.md`](./docs/oauth.md) for grant
types, token formats, scope catalog, and revocation semantics.

## Examples

Self-contained example projects live under [`examples/`](./examples/):

- `examples/oneshot-local-tool` — minimal one-shot run with a local tool.
- `examples/session-chat` — interactive REPL on top of a session.
- `examples/mixed-tools` — combines local, MANTYX, and plugin tools.
- `examples/a2a-tools` — combines `mantyx_a2a` (remote) and `define_local_a2a` (intranet) peers.
- `examples/mcp-tools` — combines `mantyx_mcp` (remote) and `define_local_mcp` (in-process).
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
