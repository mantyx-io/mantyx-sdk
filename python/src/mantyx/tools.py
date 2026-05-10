"""Public tool helpers for the MANTYX SDK.

Server-resolved (executed by MANTYX):

  * ``mantyx`` — workspace ``Tool`` row referenced by id.
  * ``mantyx_plugin`` — platform plugin tool referenced by name.
  * ``a2a`` — remote Agent2Agent peer dialed directly by MANTYX.
  * ``mcp`` — remote MCP server (Streamable HTTP) discovered + proxied by MANTYX.

Client-resolved (executed in *this* Python process; the SDK shuttles inputs
and outputs over the agent loop):

  * ``local`` — generic local tool with a Pydantic / JSON-Schema parameters spec.
  * ``a2a_local`` — A2A peer the SDK reaches on MANTYX's behalf. Pass an
    ``agent_card_url``; the SDK fetches the Agent Card and speaks A2A
    ``message/send`` for you.
  * ``mcp_local`` — MCP server fully managed by the SDK. Pass either a
    Streamable HTTP ``url`` or an ``stdio`` ``command``; the SDK runs MCP
    ``Initialize`` + ``tools/list`` and forwards ``tools/call`` for you.

The MANTYX server emits a ``local_tool_call`` event for every client-resolved
invocation; the event payload carries a ``kind`` discriminator (``"local"``
implied when omitted, ``"a2a_local"`` and ``"mcp_local"`` explicit) so the
SDK can dispatch to the right local handler.
"""

from __future__ import annotations

import asyncio
import inspect
import json
import re
from collections.abc import Awaitable, Callable, Mapping, Sequence
from dataclasses import dataclass, field
from typing import Any, Literal, TypedDict

from ._schema import ParametersInput

ToolName = str

#: Provider thinking strength. Pass either a string anchor ("off" | "low" |
#: "medium" | "high") or an integer in 0..100. ``0`` explicitly disables
#: provider thinking on reasoning models. The MANTYX server maps this onto
#: each LLM's native dial — see ``docs/agent-runs-protocol.md`` §4.4.
ReasoningLevel = Literal["off", "low", "medium", "high"] | int


class OutputSchema(TypedDict, total=False):
    """Constrains the model's final assistant text to a JSON document
    matching a JSON Schema.

    ``name`` is optional; defaults to ``"output"`` server-side. Must match
    ``/^[a-zA-Z0-9_-]{1,64}$/``. ``schema`` is required and its root must
    be a JSON object — most providers reject array / scalar roots in
    structured-output mode. The schema is shipped verbatim; MANTYX does
    not validate its contents (the provider does).

    See :func:`mantyx.parse_run_output` for the recommended client-side
    re-validation pattern.
    """

    name: str
    schema: Mapping[str, Any]


class LoopDetection(TypedDict, total=False):
    """Loop-detection thresholds.

    Both fields are optional; omitted ones inherit the MANTYX runtime
    defaults (``consecutive_threshold=3``, ``hard_cutoff_threshold=6``).
    Pass ``False`` instead of a mapping to disable the guard for the
    run / session entirely.

    * ``consecutiveThreshold`` (int ≥ 2, default ``3``) — number of
      identical consecutive tool-call batches that fires the **soft
      nudge** (the pipeline injects a steering "either deliver a final
      answer or change strategy" message before the next turn).
    * ``hardCutoffThreshold`` (int ≥ 3, default ``6``) — number of
      identical consecutive batches that forces a tools-disabled
      finalise turn. Must be **strictly greater** than
      ``consecutiveThreshold``.

    See ``docs/agent-runs-protocol.md`` §4.6.
    """

    consecutiveThreshold: int
    hardCutoffThreshold: int


class ToolBudget(TypedDict):
    """Per-tool call cap.

    ``maxCalls`` is the hard cap on executed calls per run. ``0``
    disables the tool entirely (every attempt returns the synthetic
    "budget exceeded — pivot or finalize" body on the first try).
    Server-side upper bound: ``1000`` (functionally unlimited; the
    in-runtime ``maxToolTurns: 100`` fires first).
    """

    maxCalls: int


#: Map of model-facing tool name → :class:`ToolBudget`. Pass an empty
#: dict (``{}``) to start from a clean slate (no runtime defaults applied
#: on top); omit the field entirely to keep the defaults.
ToolBudgets = Mapping[str, ToolBudget]


_LOCAL_TOOL_NAME_RE = re.compile(r"^[a-zA-Z0-9_]{1,64}$")
_PLUGIN_TOOL_NAME_RE = re.compile(r"^@[a-z][a-z0-9_-]*/[a-z][a-z0-9_-]*$")


def _assert_tool_name(name: str) -> None:
    if not isinstance(name, str) or not _LOCAL_TOOL_NAME_RE.match(name):
        raise ValueError(f"Invalid tool name {name!r}: must match /^[a-zA-Z0-9_]{{1,64}}$/")


def _prefixed_mcp_tool_name(server_name: str, tool_name: str) -> str:
    """Compose the wire-level (model-facing) tool name for a ``mcp_local``
    entry. Prepends ``<server>_`` unless the tool name already starts with
    that prefix, so manual prefixing stays idempotent."""
    prefix = f"{server_name}_"
    if tool_name.startswith(prefix):
        return tool_name
    return prefix + tool_name


# ----------------------------------------------------------- Generic local tool


@dataclass(frozen=True)
class LocalTool:
    """A tool whose handler runs inside this Python process.

    Build with :func:`define_local_tool`.
    """

    name: ToolName
    description: str = ""
    parameters: ParametersInput = None
    #: Optional JSON Schema for the structured value the tool returns.
    #: Forwarded to providers with per-tool response schemas (Gemini's
    #: ``responseJsonSchema`` on the FunctionDeclaration); other engines
    #: surface it through the description and rely on host-side
    #: validation. Same shape as ``parameters`` (Pydantic v2 ``BaseModel``,
    #: JSON Schema dict, or ``None``). See ``docs/wire-protocol.md`` §3.1.
    output_schema: ParametersInput = None
    #: When ``True``, MANTYX appends a stable hint to the model-facing
    #: description telling the model not to re-issue calls while a previous
    #: invocation is still pending. Useful for tools that may yield a
    #: ``pending`` / status response and the SDK polls on its own; without
    #: the hint, models routinely fire repeat calls and waste turns. Pure
    #: declarative — MANTYX does not change scheduling.
    long_running: bool = False
    execute: Callable[..., Any] = field(default=lambda *_: "")
    kind: str = "local"


@dataclass(frozen=True)
class MantyxToolRef:
    """Reference to a workspace ``Tool`` row resolved server-side by id."""

    id: str
    kind: str = "mantyx"


@dataclass(frozen=True)
class MantyxPluginToolRef:
    """Reference to a platform plugin tool resolved server-side by name."""

    name: str
    kind: str = "mantyx_plugin"


# ------------------------------------------------------------------------ A2A


@dataclass(frozen=True)
class MantyxA2AToolRef:
    """Reference to a remote Agent2Agent peer reachable from MANTYX
    (server-resolved). MANTYX dials ``agent_card_url`` over A2A's
    ``message/send`` RPC and forwards the reply as the tool result.
    """

    name: str
    agent_card_url: str
    description: str = ""
    headers: dict[str, str] | None = None
    context_id: str | None = None
    kind: str = "a2a"


@dataclass
class LocalA2ATool:
    """A local Agent2Agent peer fully resolved by the SDK.

    You supply only the URL of the peer's Agent Card. The SDK fetches the
    card on the first run, ships the resolved card with the spec (so MANTYX
    knows which tool is being declared), and POSTs JSON-RPC ``message/send``
    to the card's ``url`` whenever MANTYX emits a ``local_tool_call`` for
    this tool.

    Build with :func:`define_local_a2a`. The model addresses this tool by
    ``name`` and always passes ``{"message": str}`` as arguments.
    """

    name: ToolName
    agent_card_url: str
    headers: dict[str, str] | None = None
    kind: str = "a2a_local"
    # Internal: resolved Agent Card, populated lazily by the run driver on
    # the first run. Not part of the user contract.
    _resolved_card: dict[str, Any] | None = field(default=None, repr=False)


# ------------------------------------------------------------------------ MCP


@dataclass(frozen=True)
class MantyxMcpToolRef:
    """Reference to a remote MCP server (Streamable HTTP) discovered and
    proxied by MANTYX. Each tool in the catalog is exposed to the model as
    ``<name>_<tool>``; ``tool_filter`` (when set) restricts the catalog.
    """

    name: str
    url: str
    headers: dict[str, str] | None = None
    tool_filter: list[str] | None = None
    kind: str = "mcp"


@dataclass(frozen=True)
class LocalMcpHttpTransport:
    """Streamable HTTP transport spec for :func:`define_local_mcp`."""

    url: str
    headers: dict[str, str] | None = None


@dataclass(frozen=True)
class LocalMcpStdioTransport:
    """``stdio`` transport spec for :func:`define_local_mcp` — the SDK
    spawns ``command`` and speaks JSON-RPC over its stdin/stdout streams.
    """

    command: str
    args: list[str] | None = None
    env: dict[str, str] | None = None
    cwd: str | None = None


@dataclass
class LocalMcpServer:
    """A local MCP server fully managed by the SDK.

    Pass either a Streamable HTTP ``url`` or an ``stdio`` ``command``
    (mutually exclusive). On the first run, the SDK opens the transport,
    runs MCP's ``Initialize`` (capturing the ``Implementation`` block) and
    ``tools/list`` (capturing the catalog), and ships both inline as part
    of the spec. On every ``local_tool_call`` with ``kind: "mcp_local"``
    the SDK forwards the call to MCP ``tools/call`` on the cached
    connection and POSTs the flattened text response back to MANTYX.

    Connections are reused across runs and across messages within a
    session, and closed on ``run_agent`` completion / ``session.end()``.
    """

    name: str
    http: LocalMcpHttpTransport | None = None
    stdio: LocalMcpStdioTransport | None = None
    kind: str = "mcp_local"
    # Internal: resolved snapshot (server_info + tools list) plus the live
    # MCP client + close hook. Populated lazily by the run driver. Not part
    # of the user contract.
    _resolved: _ResolvedMcpServer | None = field(default=None, repr=False)


@dataclass
class _ResolvedMcpServer:
    """Internal — the live MCP client + the snapshot we ship on the wire.

    The ``call`` and ``close`` callables wrap whichever runtime owns the
    transport (a per-client BlockingPortal for the sync client, the
    caller's own event loop for the async client) so the dispatch path
    can be runtime-agnostic.
    """

    server_info: dict[str, Any]
    tools: list[dict[str, Any]]  # verbatim `tools/list` entries (with `inputSchema`)
    call_async: Callable[[str, dict[str, Any]], Awaitable[str]]
    aclose: Callable[[], Awaitable[None]]
    # When the resolved server is owned by the synchronous client we keep
    # blocking shims here too so the dispatch path can use plain calls.
    call_sync: Callable[[str, dict[str, Any]], str] | None = None
    close_sync: Callable[[], None] | None = None


# --------------------------------------------------------------- Public unions

ToolRef = (
    LocalTool
    | LocalA2ATool
    | LocalMcpServer
    | MantyxToolRef
    | MantyxPluginToolRef
    | MantyxA2AToolRef
    | MantyxMcpToolRef
)


# ---------------------------------------------------------------- Constructors


def define_local_tool(
    *,
    name: ToolName,
    execute: Callable[..., Any],
    description: str = "",
    parameters: ParametersInput = None,
    output_schema: ParametersInput = None,
    long_running: bool = False,
) -> LocalTool:
    """Define a tool whose handler runs in *this* Python process.

    Args:
        name: ``[a-zA-Z0-9_]{1,64}`` — what the model addresses the tool by.
        execute: Sync or async callable. Receives parsed args (a Pydantic
            model instance if ``parameters`` is a model, otherwise a
            ``dict``). Must return a string. Non-string returns are
            JSON-serialised by the SDK before being POSTed back.
        description: Free-form description for the model.
        parameters: A Pydantic v2 ``BaseModel`` subclass, a JSON Schema
            dict, or ``None`` for "any object".
        output_schema: Optional JSON Schema (or Pydantic model) describing
            the tool's structured return value. Forwarded to providers
            with per-tool response schemas; other engines surface it
            through the description. The model uses it to plan follow-up
            arguments more reliably. See ``docs/wire-protocol.md`` §3.1.
        long_running: When ``True``, MANTYX annotates the model-facing
            description with a hint instructing the model not to re-issue
            calls while a previous invocation is still pending. Useful
            for tools that may yield a ``pending`` / status response and
            the SDK polls on its own.
    """
    _assert_tool_name(name)
    return LocalTool(
        name=name,
        description=description or "",
        parameters=parameters,
        output_schema=output_schema,
        long_running=bool(long_running),
        execute=execute,
    )


def mantyx_tool(tool_id: str) -> MantyxToolRef:
    """Reference a workspace ``Tool`` by its ``tool_<cuid>`` id."""
    if not isinstance(tool_id, str) or not tool_id:
        raise ValueError("mantyx_tool(id): id must be a non-empty string")
    return MantyxToolRef(id=tool_id)


def mantyx_plugin_tool(name: str) -> MantyxPluginToolRef:
    """Reference an installed platform plugin tool by ``@plugin-slug/tool-name``."""
    if not isinstance(name, str) or not _PLUGIN_TOOL_NAME_RE.match(name):
        raise ValueError(
            f"mantyx_plugin_tool(name): expected '@plugin-slug/tool-name', got {name!r}"
        )
    return MantyxPluginToolRef(name=name)


def mantyx_a2a(
    *,
    name: str,
    agent_card_url: str,
    description: str = "",
    headers: dict[str, str] | None = None,
    context_id: str | None = None,
) -> MantyxA2AToolRef:
    """Reference a remote Agent2Agent peer reachable from MANTYX.

    Args:
        name: Tool name surfaced to the model (``[a-zA-Z0-9_]{1,64}``).
        agent_card_url: Remote Agent Card URL or JSON-RPC root.
        description: Model-facing description (defaults to a generic hint).
        headers: Per-request HTTP headers (typically ``Authorization``).
            Forwarded as-is — for long-lived credentials, register the peer
            as a workspace ``ExternalAgent`` instead.
        context_id: Optional A2A ``contextId`` to thread multiple
            delegations into the same remote conversation.
    """
    _assert_tool_name(name)
    if not isinstance(agent_card_url, str) or not agent_card_url:
        raise ValueError("mantyx_a2a: agent_card_url is required")
    return MantyxA2AToolRef(
        name=name,
        agent_card_url=agent_card_url,
        description=description or "",
        headers=dict(headers) if headers else None,
        context_id=context_id,
    )


def define_local_a2a(
    *,
    name: str,
    agent_card_url: str,
    headers: dict[str, str] | None = None,
) -> LocalA2ATool:
    """Define a local Agent2Agent peer — URL only.

    On the first ``run_agent`` / ``create_session`` the SDK fetches the
    Agent Card from ``agent_card_url`` (using ``headers``), ships the
    resolved card with the spec, and uses ``agent_card.url`` as the
    target for subsequent ``message/send`` POSTs.

    Args:
        name: Tool name surfaced to the model (``[a-zA-Z0-9_]{1,64}``).
        agent_card_url: URL of the peer's Agent Card
            (``/.well-known/agent-card.json`` is the conventional path).
        headers: HTTP headers attached to **both** the card fetch and every
            ``message/send`` POST (typically ``Authorization`` for intranet
            peers).
    """
    _assert_tool_name(name)
    if not isinstance(agent_card_url, str) or not agent_card_url:
        raise ValueError("define_local_a2a: `agent_card_url` is required")
    return LocalA2ATool(
        name=name,
        agent_card_url=agent_card_url,
        headers=dict(headers) if headers else None,
    )


def mantyx_mcp(
    *,
    name: str,
    url: str,
    headers: dict[str, str] | None = None,
    tool_filter: Sequence[str] | None = None,
) -> MantyxMcpToolRef:
    """Reference a remote MCP server (Streamable HTTP) reachable from MANTYX."""
    _assert_tool_name(name)
    if not isinstance(url, str) or not url:
        raise ValueError("mantyx_mcp: url is required")
    return MantyxMcpToolRef(
        name=name,
        url=url,
        headers=dict(headers) if headers else None,
        tool_filter=list(tool_filter) if tool_filter else None,
    )


def define_local_mcp(
    *,
    name: str,
    # Streamable HTTP transport
    url: str | None = None,
    headers: dict[str, str] | None = None,
    # stdio transport
    command: str | None = None,
    args: Sequence[str] | None = None,
    env: dict[str, str] | None = None,
    cwd: str | None = None,
) -> LocalMcpServer:
    """Define a local MCP server.

    Pass exactly one of:

    * ``url`` (Streamable HTTP MCP endpoint), optionally with ``headers``.
    * ``command`` (stdio executable to spawn), optionally with ``args``,
      ``env``, and ``cwd``.

    The SDK opens the transport on the first ``run_agent`` /
    ``create_session``, runs MCP ``Initialize`` + ``tools/list`` to capture
    the catalog and ``Implementation`` block, and forwards every
    ``local_tool_call`` into MCP ``tools/call``. Each tool's wire-level
    name is ``<this server's name>_<tool>`` so the model sees the same
    surface MANTYX produces for ``kind: "mcp"``.
    """
    _assert_tool_name(name)
    has_http = isinstance(url, str) and bool(url)
    has_stdio = isinstance(command, str) and bool(command)
    if has_http and has_stdio:
        raise ValueError(
            "define_local_mcp: pass either `url` (Streamable HTTP) or `command` (stdio), not both"
        )
    if not has_http and not has_stdio:
        raise ValueError(
            "define_local_mcp: one of `url` (Streamable HTTP) or `command` (stdio) is required"
        )
    if has_http:
        assert url is not None
        return LocalMcpServer(
            name=name,
            http=LocalMcpHttpTransport(url=url, headers=dict(headers) if headers else None),
        )
    assert command is not None
    return LocalMcpServer(
        name=name,
        stdio=LocalMcpStdioTransport(
            command=command,
            args=list(args) if args else None,
            env=dict(env) if env else None,
            cwd=cwd,
        ),
    )


# --------------------------------------------------------------- Type-guards


def is_local_tool(t: ToolRef) -> bool:
    return isinstance(t, LocalTool)


def is_local_a2a_tool(t: ToolRef) -> bool:
    return isinstance(t, LocalA2ATool)


def is_local_mcp_server(t: ToolRef) -> bool:
    return isinstance(t, LocalMcpServer)


# ----------------------------------------------------------------- Internals


@dataclass
class _LocalHandlers:
    """Internal registry of client-resolved handlers, indexed by ``kind``.

    For ``a2a_local`` and ``mcp_local`` the registry just maps name → ref;
    the resolved snapshot lives on the ref itself (populated by the run
    driver before submission), so dispatch only needs to find the ref.
    """

    local_tools: dict[ToolName, LocalTool] = field(default_factory=dict)
    a2a_tools: dict[ToolName, LocalA2ATool] = field(default_factory=dict)
    mcp_servers: dict[str, LocalMcpServer] = field(default_factory=dict)


async def maybe_await(value: Awaitable[Any] | Any) -> Any:
    """Helper: awaits a coroutine, otherwise returns the value as-is."""
    if inspect.isawaitable(value):
        return await value
    return value


def call_handler_sync(handler: Callable[..., Any], parsed_args: Any) -> Any:
    """Invoke ``handler(parsed_args)``; if it returns a coroutine, run it
    on a fresh event loop. Used by the synchronous client."""
    out = handler(parsed_args)
    if inspect.isawaitable(out):
        return asyncio.run(maybe_await(out))
    return out


def collect_local_handlers(
    tools: list[ToolRef] | None,
) -> _LocalHandlers:
    """Build the registry the run loop dispatches against, partitioned by
    discriminator (``local`` / ``a2a_local`` / ``mcp_local``)."""
    out = _LocalHandlers()
    if not tools:
        return out
    for t in tools:
        if isinstance(t, LocalTool):
            out.local_tools[t.name] = t
        elif isinstance(t, LocalA2ATool):
            out.a2a_tools[t.name] = t
        elif isinstance(t, LocalMcpServer):
            out.mcp_servers[t.name] = t
    return out


def serialize_tool_refs(tools: list[ToolRef] | None) -> list[dict[str, Any]]:
    """Translate the in-process ``ToolRef`` list into the wire dict shape.

    Local-A2A and local-MCP refs **must** have been resolved before this
    is called (the run driver is responsible for that); otherwise the
    SDK has no Agent Card / MCP catalog to put on the wire.
    """
    from ._schema import to_tool_parameters_wire

    if not tools:
        return []
    out: list[dict[str, Any]] = []
    for t in tools:
        if isinstance(t, MantyxToolRef):
            out.append({"kind": "mantyx", "id": t.id})
        elif isinstance(t, MantyxPluginToolRef):
            out.append({"kind": "mantyx_plugin", "name": t.name})
        elif isinstance(t, LocalTool):
            local_entry: dict[str, Any] = {
                "kind": "local",
                "name": t.name,
                "description": t.description,
                "parameters": to_tool_parameters_wire(t.parameters),
            }
            if t.output_schema is not None:
                local_entry["outputSchema"] = to_tool_parameters_wire(t.output_schema)
            if t.long_running:
                local_entry["longRunning"] = True
            out.append(local_entry)
        elif isinstance(t, MantyxA2AToolRef):
            entry: dict[str, Any] = {
                "kind": "a2a",
                "name": t.name,
                "agentCardUrl": t.agent_card_url,
            }
            if t.description:
                entry["description"] = t.description
            if t.headers:
                entry["headers"] = dict(t.headers)
            if t.context_id:
                entry["contextId"] = t.context_id
            out.append(entry)
        elif isinstance(t, LocalA2ATool):
            if t._resolved_card is None:
                raise ValueError(
                    f"define_local_a2a({t.name!r}): agent card has not been "
                    "resolved yet (was `run_agent` / `create_session` skipped?)"
                )
            out.append(
                {
                    "kind": "a2a_local",
                    "name": t.name,
                    "agentCard": dict(t._resolved_card),
                }
            )
        elif isinstance(t, MantyxMcpToolRef):
            entry = {
                "kind": "mcp",
                "name": t.name,
                "url": t.url,
            }
            if t.headers:
                entry["headers"] = dict(t.headers)
            if t.tool_filter:
                entry["toolFilter"] = list(t.tool_filter)
            out.append(entry)
        elif isinstance(t, LocalMcpServer):
            if t._resolved is None:
                raise ValueError(
                    f"define_local_mcp({t.name!r}): MCP server has not been initialised yet"
                )
            wire_tools: list[dict[str, Any]] = []
            for tool in t._resolved.tools:
                tool_name_raw = str(tool.get("name") or "")
                wire_entry: dict[str, Any] = {
                    "name": _prefixed_mcp_tool_name(t.name, tool_name_raw),
                    "inputSchema": dict(tool.get("inputSchema") or {"type": "object"}),
                }
                description = tool.get("description")
                if isinstance(description, str) and description:
                    wire_entry["description"] = description
                annotations = tool.get("annotations")
                if isinstance(annotations, dict):
                    wire_entry["annotations"] = dict(annotations)
                wire_tools.append(wire_entry)
            out.append(
                {
                    "kind": "mcp_local",
                    "name": t.name,
                    "serverInfo": dict(t._resolved.server_info),
                    "tools": wire_tools,
                }
            )
        else:  # pragma: no cover — defensive
            raise TypeError(f"Unknown ToolRef kind: {type(t).__name__}")
    return out


def normalize_reasoning_level(level: ReasoningLevel | None) -> str | int | None:
    """Validate and coerce a :data:`ReasoningLevel` for the wire."""
    if level is None:
        return None
    if isinstance(level, bool):
        # ``bool`` is a subclass of ``int`` — reject it explicitly so a typo
        # like ``reasoning_level=True`` doesn't silently get sent as 1.
        raise ValueError(
            "reasoning_level must be 'off' | 'low' | 'medium' | 'high' or an int 0..100"
        )
    if isinstance(level, int):
        if level < 0 or level > 100:
            raise ValueError(f"reasoning_level integer must be in [0, 100]; got {level}")
        return int(level)
    if isinstance(level, str) and level in ("off", "low", "medium", "high"):
        return level
    raise ValueError(
        "reasoning_level must be one of 'off' | 'low' | 'medium' | 'high' "
        f"or an int 0..100; got {level!r}"
    )


_OUTPUT_SCHEMA_NAME_RE = re.compile(r"^[a-zA-Z0-9_-]{1,64}$")
_OUTPUT_SCHEMA_MAX_BYTES = 32 * 1024

_LOOP_DETECTION_THRESHOLD_MAX = 100
_TOOL_BUDGETS_MAX_ENTRIES = 32
_TOOL_BUDGET_MAX_NAME_LEN = 120
_TOOL_BUDGET_MAX_CALLS = 1000


def normalize_loop_detection(
    value: LoopDetection | Mapping[str, Any] | bool | None,
) -> dict[str, Any] | bool | None:
    """Validate a :class:`LoopDetection` (or ``False``) value and return
    the wire-shaped value.

    Mirrors the server-side ``400 invalid_request`` checks (thresholds in
    range, hard cutoff strictly greater than consecutive) so callers see
    an early local error.

    * ``None`` → ``None`` (field omitted; runtime defaults apply).
    * ``False`` → ``False`` (guard explicitly disabled).
    * mapping → validated, wire-shaped dict.

    A value of ``True`` is rejected — it would otherwise quietly serialize
    as ``true`` on the wire, which the server rejects.
    """
    if value is None:
        return None
    if isinstance(value, bool):
        if value is False:
            return False
        raise ValueError(
            "loop_detection must be a mapping or the literal False; True is not a valid value"
        )
    if not isinstance(value, Mapping):
        raise ValueError(
            "loop_detection must be a mapping of shape "
            "{'consecutiveThreshold'?: int, 'hardCutoffThreshold'?: int} "
            f"or False; got {type(value).__name__}"
        )
    out: dict[str, Any] = {}
    consec_raw = value.get("consecutiveThreshold")
    if consec_raw is None:
        consec_raw = value.get("consecutive_threshold")
    hard_raw = value.get("hardCutoffThreshold")
    if hard_raw is None:
        hard_raw = value.get("hard_cutoff_threshold")
    if consec_raw is not None:
        out["consecutiveThreshold"] = _assert_threshold(
            "loop_detection.consecutive_threshold", consec_raw, minimum=2
        )
    if hard_raw is not None:
        out["hardCutoffThreshold"] = _assert_threshold(
            "loop_detection.hard_cutoff_threshold", hard_raw, minimum=3
        )
    consec = out.get("consecutiveThreshold")
    hard = out.get("hardCutoffThreshold")
    if isinstance(consec, int) and isinstance(hard, int) and hard <= consec:
        raise ValueError(
            f"loop_detection.hard_cutoff_threshold ({hard}) must be strictly "
            f"greater than loop_detection.consecutive_threshold ({consec})"
        )
    return out


def _assert_threshold(label: str, value: Any, *, minimum: int) -> int:
    if isinstance(value, bool) or not isinstance(value, int):
        raise ValueError(f"{label} must be an integer; got {value!r}")
    if value < minimum:
        raise ValueError(f"{label} must be >= {minimum}; got {value}")
    if value > _LOOP_DETECTION_THRESHOLD_MAX:
        raise ValueError(
            f"{label} must be <= {_LOOP_DETECTION_THRESHOLD_MAX} (server-enforced); got {value}"
        )
    return value


def normalize_tool_budgets(
    value: ToolBudgets | Mapping[str, Mapping[str, Any]] | None,
) -> dict[str, dict[str, int]] | None:
    """Validate a :class:`ToolBudgets` value and return the wire-shaped dict.

    Mirrors the server-side ``400 invalid_request`` checks (max 32
    entries; key length 1..120; ``maxCalls`` ≥ 0 and ≤ 1000) so callers
    see an early local error. An empty mapping (``{}``) is valid and
    signals "clear the runtime defaults"; pass ``None`` to keep them.
    """
    if value is None:
        return None
    if not isinstance(value, Mapping):
        raise ValueError(
            "tool_budgets must be a mapping of shape "
            "{tool_name: {'maxCalls': int}}; "
            f"got {type(value).__name__}"
        )
    if len(value) > _TOOL_BUDGETS_MAX_ENTRIES:
        raise ValueError(
            f"tool_budgets has {len(value)} entries; "
            f"the server enforces a {_TOOL_BUDGETS_MAX_ENTRIES}-entry limit"
        )
    out: dict[str, dict[str, int]] = {}
    for key, entry in value.items():
        if not isinstance(key, str) or not (1 <= len(key) <= _TOOL_BUDGET_MAX_NAME_LEN):
            raise ValueError(
                f"tool_budgets keys must be 1..{_TOOL_BUDGET_MAX_NAME_LEN}-char "
                f"strings; got {key!r}"
            )
        if not isinstance(entry, Mapping):
            raise ValueError(
                f"tool_budgets[{key!r}] must be a mapping {{'maxCalls': int}}; "
                f"got {type(entry).__name__}"
            )
        max_calls_raw = entry.get("maxCalls")
        if max_calls_raw is None:
            max_calls_raw = entry.get("max_calls")
        if max_calls_raw is None:
            raise ValueError(f"tool_budgets[{key!r}].maxCalls is required")
        if isinstance(max_calls_raw, bool) or not isinstance(max_calls_raw, int):
            raise ValueError(
                f"tool_budgets[{key!r}].maxCalls must be a non-negative integer; "
                f"got {max_calls_raw!r}"
            )
        if max_calls_raw < 0:
            raise ValueError(f"tool_budgets[{key!r}].maxCalls must be >= 0; got {max_calls_raw}")
        if max_calls_raw > _TOOL_BUDGET_MAX_CALLS:
            raise ValueError(
                f"tool_budgets[{key!r}].maxCalls must be <= {_TOOL_BUDGET_MAX_CALLS} "
                f"(server-enforced); got {max_calls_raw}"
            )
        out[key] = {"maxCalls": int(max_calls_raw)}
    return out


def normalize_output_schema(
    value: OutputSchema | Mapping[str, Any] | None,
) -> dict[str, Any] | None:
    """Validate an :class:`OutputSchema` value and return the wire-shaped dict.

    Mirrors the server-side ``400 invalid_request`` checks (name regex,
    schema shape, ≤ 32 KB serialised) so callers get an early local error
    instead of a round-trip rejection.
    """
    if value is None:
        return None
    if not isinstance(value, Mapping):
        raise ValueError(
            "output_schema must be a mapping of shape {'name'?: str, 'schema': dict}; "
            f"got {type(value).__name__}"
        )
    out: dict[str, Any] = {}
    if "name" in value and value["name"] is not None:
        name = value["name"]
        if not isinstance(name, str) or not _OUTPUT_SCHEMA_NAME_RE.match(name):
            raise ValueError(
                f"output_schema.name must match /^[a-zA-Z0-9_-]{{1,64}}$/; got {name!r}"
            )
        out["name"] = name
    if "schema" not in value:
        raise ValueError("output_schema.schema is required")
    schema = value["schema"]
    if not isinstance(schema, Mapping) or isinstance(schema, (list, tuple)):
        raise ValueError(
            "output_schema.schema must be a non-null JSON object (the JSON Schema root)"
        )
    out["schema"] = dict(schema) if not isinstance(schema, dict) else schema
    try:
        serialized = json.dumps(out)
    except (TypeError, ValueError) as exc:
        raise ValueError(f"output_schema is not JSON-serialisable: {exc}") from exc
    if len(serialized.encode("utf-8")) > _OUTPUT_SCHEMA_MAX_BYTES:
        raise ValueError(
            f"output_schema serialised JSON is {len(serialized)} bytes; "
            "the server enforces a 32 KB limit"
        )
    return out


__all__ = [
    "LocalA2ATool",
    "LocalMcpHttpTransport",
    "LocalMcpServer",
    "LocalMcpStdioTransport",
    "LocalTool",
    "LoopDetection",
    "MantyxA2AToolRef",
    "MantyxMcpToolRef",
    "MantyxPluginToolRef",
    "MantyxToolRef",
    "OutputSchema",
    "ReasoningLevel",
    "ToolBudget",
    "ToolBudgets",
    "ToolName",
    "ToolRef",
    "call_handler_sync",
    "collect_local_handlers",
    "define_local_a2a",
    "define_local_mcp",
    "define_local_tool",
    "is_local_a2a_tool",
    "is_local_mcp_server",
    "is_local_tool",
    "mantyx_a2a",
    "mantyx_mcp",
    "mantyx_plugin_tool",
    "mantyx_tool",
    "maybe_await",
    "normalize_loop_detection",
    "normalize_output_schema",
    "normalize_reasoning_level",
    "normalize_tool_budgets",
    "serialize_tool_refs",
]
