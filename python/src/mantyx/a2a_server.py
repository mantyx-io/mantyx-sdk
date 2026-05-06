"""Expose a MANTYX agent over the Agent2Agent (A2A) protocol.

This module is opt-in — it depends on the official ``a2a-sdk`` package
(plus its ``http-server`` extra for the Starlette/uvicorn integration).
Install both with::

    pip install "mantyx-sdk[a2a-server]"

Quickstart::

    from mantyx import AsyncMantyxClient
    from mantyx.a2a_server import (
        MantyxAgentExecutor,
        serve_agent_over_a2a,
        build_agent_card,
    )

    async def main() -> None:
        async with AsyncMantyxClient(api_key="...", workspace_slug="acme") as client:
            handle = await serve_agent_over_a2a(
                client=client,
                port=4000,
                agent_card=build_agent_card(
                    name="Acme Support",
                    description="Customer support agent.",
                    version="1.0.0",
                    public_url="http://localhost:4000",
                ),
                agent_id="agent_cm6abc123",   # or system_prompt=...
            )
            await handle.serve_forever()

The resulting server publishes:

- the Agent Card at ``GET /.well-known/agent-card.json``;
- A2A JSON-RPC at the root path (``message/send``, ``message/stream``,
  ``tasks/get``, …);
- A2A HTTP+JSON/REST at ``/v1/`` (when not disabled).

Each incoming A2A ``contextId`` is mapped to a long-lived MANTYX session by
default, so multi-turn conversations share history without any extra
plumbing. Set ``conversation="stateless"`` to reduce every A2A request to a
one-shot ``run_agent`` call.
"""

from __future__ import annotations

import asyncio
import logging
import uuid
from collections import OrderedDict
from collections.abc import Awaitable, Callable, Mapping, Sequence
from contextlib import suppress
from dataclasses import dataclass
from datetime import datetime, timezone
from typing import Any

try:
    # The optional ``a2a-sdk`` (>= 1.0) brings in the official A2A protocol
    # types, server primitives, and Starlette route helpers. We import lazily
    # at module load so that simply importing this submodule fails with a
    # clear actionable error instead of an obscure ImportError later.
    from a2a.server.agent_execution import AgentExecutor as _BaseAgentExecutor
    from a2a.server.agent_execution import RequestContext
    from a2a.server.events import EventQueue
    from a2a.server.request_handlers import DefaultRequestHandler
    from a2a.server.routes import (
        create_agent_card_routes,
        create_jsonrpc_routes,
        create_rest_routes,
    )
    from a2a.server.tasks import InMemoryTaskStore
    from a2a.types import (
        AgentCard,
        AgentSkill,
        Message,
        Role,
        Task,
        TaskState,
        TaskStatusUpdateEvent,
    )
except ImportError as exc:  # pragma: no cover - import guard
    raise ImportError(
        "mantyx.a2a_server requires the optional A2A extras. Install them with: "
        'pip install "mantyx-sdk[a2a-server]"'
    ) from exc

from .async_client import AsyncAgentSession, AsyncMantyxClient
from .errors import MantyxError, MantyxRunError
from .tools import ReasoningLevel, ToolRef

__all__ = [
    "MantyxAgentExecutor",
    "MantyxAgentSpec",
    "ServeHandle",
    "build_agent_card",
    "serve_agent_over_a2a",
]

_log = logging.getLogger(__name__)


# --------------------------------------------------------------- Public API


@dataclass
class MantyxAgentSpec:
    """Description of the MANTYX agent that should answer A2A requests.

    Either ``agent_id`` (persisted MANTYX agent) or ``system_prompt``
    (ephemeral inline agent) must be set. The remaining fields mirror the
    arguments of :meth:`mantyx.AsyncMantyxClient.run_agent` /
    :meth:`mantyx.AsyncMantyxClient.create_session`.
    """

    agent_id: str | None = None
    system_prompt: str | None = None
    model_id: str | None = None
    name: str | None = None
    tools: Sequence[ToolRef] | None = None
    reasoning_level: ReasoningLevel | None = None
    metadata: Mapping[str, str] | None = None
    budgets: Mapping[str, Any] | None = None

    def __post_init__(self) -> None:
        if not self.agent_id and not self.system_prompt:
            raise MantyxError(
                "MantyxAgentSpec: either `agent_id` or `system_prompt` is required",
            )


@dataclass
class ServeHandle:
    """Result returned by :func:`serve_agent_over_a2a`."""

    url: str
    port: int
    _server: Any  # uvicorn.Server
    _task: asyncio.Task[Any]
    _executor: MantyxAgentExecutor

    async def serve_forever(self) -> None:
        """Block until the server stops (e.g. on Ctrl-C)."""
        await self._task

    async def aclose(self) -> None:
        """Stop the HTTP server, end every cached MANTYX session."""
        self._server.should_exit = True
        with suppress(asyncio.CancelledError):
            await self._task
        await self._executor.aclose()


def build_agent_card(
    *,
    name: str,
    description: str,
    version: str,
    public_url: str,
    skills: Sequence[Mapping[str, Any]] | None = None,
    streaming: bool = True,
    push_notifications: bool = False,
    default_input_modes: Sequence[str] = ("text",),
    default_output_modes: Sequence[str] = ("text",),
) -> AgentCard:
    """Build a minimal :class:`a2a.types.AgentCard` from primitive args.

    The Python A2A SDK's :class:`AgentCard` is a protobuf message which is
    awkward to construct field-by-field; this helper hides that boilerplate
    for the common case. Pass the result straight into
    :func:`serve_agent_over_a2a`. Mutate the returned card if you need to
    customise fields the helper doesn't expose (e.g. ``provider``,
    ``security_schemes``).
    """
    card = AgentCard()
    card.name = name
    card.description = description
    card.version = version
    if skills is None:
        skills = [
            {"id": "chat", "name": "Chat", "description": description, "tags": ["chat"]}
        ]
    for s in skills:
        sk = AgentSkill()
        sk.id = str(s["id"])
        sk.name = str(s["name"])
        if s.get("description"):
            sk.description = str(s["description"])
        for tag in s.get("tags", []) or []:
            sk.tags.append(str(tag))
        card.skills.append(sk)
    for m in default_input_modes:
        card.default_input_modes.append(m)
    for m in default_output_modes:
        card.default_output_modes.append(m)
    card.capabilities.streaming = bool(streaming)
    card.capabilities.push_notifications = bool(push_notifications)
    # Record the public URL on a JSON-RPC interface declaration.
    iface = card.supported_interfaces.add()
    iface.url = public_url
    iface.protocol_binding = "JSONRPC"
    return card


# --------------------------------------------------------- Implementation


class MantyxAgentExecutor(_BaseAgentExecutor):
    """A2A :class:`AgentExecutor` that routes turns into a MANTYX agent.

    Most callers want :func:`serve_agent_over_a2a` instead; reach for this
    class directly to mount the executor in your own Starlette / FastAPI /
    grpc app. It implements both ``execute`` and ``cancel`` and tracks an
    LRU map of A2A ``context_id`` to live :class:`AsyncAgentSession`
    instances so multi-turn conversations share history.
    """

    def __init__(
        self,
        *,
        client: AsyncMantyxClient,
        agent: MantyxAgentSpec,
        conversation: str = "auto",
        max_sessions: int = 1024,
        on_assistant_delta: Callable[[str, RequestContext, EventQueue], Awaitable[None] | None]
        | None = None,
    ) -> None:
        if conversation not in ("auto", "stateless"):
            raise MantyxError(
                f"MantyxAgentExecutor: invalid `conversation` value: {conversation!r}",
            )
        self.client = client
        self.agent = agent
        self.conversation = conversation
        self.max_sessions = max_sessions
        self.on_assistant_delta = on_assistant_delta
        # context_id -> live AsyncAgentSession; OrderedDict gives us LRU
        # eviction by re-inserting on every hit.
        self._sessions: OrderedDict[str, AsyncAgentSession] = OrderedDict()
        # Tasks we have been told to cancel; consulted by execute().
        self._cancelled: set[str] = set()

    # ----------------------------------------------------------- AgentExecutor

    async def execute(self, context: RequestContext, event_queue: EventQueue) -> None:
        task_id = context.task_id or _uuid()
        context_id = context.context_id or _uuid()
        user_text = context.get_user_input()

        if context.current_task is None:
            await event_queue.enqueue_event(
                _build_initial_task(task_id, context_id, context.message),
            )
        await event_queue.enqueue_event(
            _build_status_update(
                task_id, context_id, TaskState.TASK_STATE_WORKING, final=False
            ),
        )

        if task_id in self._cancelled:
            self._cancelled.discard(task_id)
            await event_queue.enqueue_event(
                _build_status_update(
                    task_id,
                    context_id,
                    TaskState.TASK_STATE_CANCELED,
                    final=True,
                    text="Cancelled before run started.",
                ),
            )
            return

        async def _on_delta(delta: str) -> None:
            if self.on_assistant_delta is not None:
                result = self.on_assistant_delta(delta, context, event_queue)
                if asyncio.iscoroutine(result):
                    await result
                return
            await event_queue.enqueue_event(
                _build_delta_status_update(task_id, context_id, delta)
            )

        try:
            text = await self._run_once(context_id, user_text, _on_delta)
        except asyncio.CancelledError:
            await event_queue.enqueue_event(
                _build_status_update(
                    task_id, context_id, TaskState.TASK_STATE_CANCELED, final=True
                ),
            )
            raise
        except MantyxRunError as exc:
            _log.warning("MANTYX run failed: %s", exc)
            await event_queue.enqueue_event(
                _build_status_update(
                    task_id,
                    context_id,
                    TaskState.TASK_STATE_FAILED,
                    final=True,
                    text=f"MANTYX run failed ({exc.subtype or 'unknown'}): {exc}",
                ),
            )
            return
        except Exception as exc:
            _log.exception("Executor error")
            await event_queue.enqueue_event(
                _build_status_update(
                    task_id,
                    context_id,
                    TaskState.TASK_STATE_FAILED,
                    final=True,
                    text=f"Executor error: {exc}",
                ),
            )
            return

        await event_queue.enqueue_event(
            _build_status_update(
                task_id,
                context_id,
                TaskState.TASK_STATE_COMPLETED,
                final=True,
                text=text,
            ),
        )

    async def cancel(self, context: RequestContext, event_queue: EventQueue) -> None:
        task_id = context.task_id or ""
        if task_id:
            self._cancelled.add(task_id)
        # The active execute() call publishes the final 'canceled' status
        # itself; we only mark intent here. A fully cooperative cancel
        # implementation would also abort the in-flight HTTP call — left
        # as a future enhancement.

    # ----------------------------------------------------------- Lifecycle

    async def aclose(self) -> None:
        """End every cached MANTYX session. Idempotent."""
        sessions = list(self._sessions.values())
        self._sessions.clear()
        for s in sessions:
            with suppress(Exception):
                await s.end()

    # ----------------------------------------------------------- Internals

    async def _run_once(
        self,
        context_id: str,
        prompt: str,
        on_delta: Callable[[str], Awaitable[None]],
    ) -> str:
        if self.conversation == "stateless":
            kwargs = _spec_to_run_kwargs(self.agent)
            result = await self.client.run_agent(
                prompt=prompt,
                on_assistant_delta=on_delta,
                **kwargs,
            )
            return result.text or ""

        session = await self._get_or_create_session(context_id)
        result = await session.send(prompt, on_assistant_delta=on_delta)
        return result.text or ""

    async def _get_or_create_session(self, context_id: str) -> AsyncAgentSession:
        existing = self._sessions.get(context_id)
        if existing is not None:
            self._sessions.move_to_end(context_id)
            return existing
        kwargs = _spec_to_session_kwargs(self.agent, context_id)
        session = await self.client.create_session(**kwargs)
        self._sessions[context_id] = session
        await self._evict_if_needed()
        return session

    async def _evict_if_needed(self) -> None:
        while len(self._sessions) > self.max_sessions:
            _, oldest = self._sessions.popitem(last=False)
            with suppress(Exception):
                await oldest.end()


async def serve_agent_over_a2a(
    *,
    client: AsyncMantyxClient,
    agent_card: AgentCard,
    agent_id: str | None = None,
    system_prompt: str | None = None,
    model_id: str | None = None,
    tools: Sequence[ToolRef] | None = None,
    reasoning_level: ReasoningLevel | None = None,
    metadata: Mapping[str, str] | None = None,
    budgets: Mapping[str, Any] | None = None,
    name: str | None = None,
    conversation: str = "auto",
    max_sessions: int = 1024,
    host: str = "0.0.0.0",
    port: int = 0,
    rpc_url: str = "/",
    rest: bool = True,
    rest_path_prefix: str = "/v1",
    on_assistant_delta: Callable[[str, RequestContext, EventQueue], Awaitable[None] | None]
    | None = None,
) -> ServeHandle:
    """Spin up an A2A HTTP server that wraps a MANTYX agent.

    Returns a :class:`ServeHandle` whose ``serve_forever()`` blocks until the
    server is asked to stop (Ctrl-C, ``aclose()``, …) and whose ``aclose()``
    tears the server and every cached MANTYX session down.

    Mounts:

    * ``GET /.well-known/agent-card.json`` — the Agent Card.
    * ``rpc_url`` (default ``"/"``) — the JSON-RPC endpoint.
    * ``rest_path_prefix + "/v1"`` (default ``"/v1"``) — the HTTP+JSON/REST
      endpoint. Pass ``rest=False`` to omit it.
    """
    # Late import — uvicorn is part of the [a2a-server] extra; importing it
    # at module load would defeat the optional-dependency story.
    try:
        import uvicorn
        from starlette.applications import Starlette
    except ImportError as exc:  # pragma: no cover
        raise ImportError(
            "serve_agent_over_a2a requires `uvicorn` and `starlette`. Install "
            'them with: pip install "mantyx-sdk[a2a-server]"',
        ) from exc

    spec = MantyxAgentSpec(
        agent_id=agent_id,
        system_prompt=system_prompt,
        model_id=model_id,
        name=name,
        tools=tools,
        reasoning_level=reasoning_level,
        metadata=metadata,
        budgets=budgets,
    )

    executor = MantyxAgentExecutor(
        client=client,
        agent=spec,
        conversation=conversation,
        max_sessions=max_sessions,
        on_assistant_delta=on_assistant_delta,
    )

    handler = DefaultRequestHandler(
        agent_executor=executor,
        task_store=InMemoryTaskStore(),
        agent_card=agent_card,
    )

    routes: list[Any] = []
    routes.extend(create_agent_card_routes(agent_card))
    routes.extend(create_jsonrpc_routes(handler, rpc_url=rpc_url))
    if rest:
        routes.extend(create_rest_routes(handler, path_prefix=rest_path_prefix))
    app = Starlette(routes=routes)

    config = uvicorn.Config(app=app, host=host, port=port, log_level="info")
    server = uvicorn.Server(config)

    task = asyncio.create_task(server.serve())
    # Wait until uvicorn binds the listener so the caller can introspect the
    # actual port (useful when port=0).
    while not getattr(server, "started", False):
        if task.done():
            # Surface startup errors immediately.
            await task
            raise MantyxError("uvicorn server exited before startup completed")
        await asyncio.sleep(0.01)

    bound_port = port
    for srv in getattr(server, "servers", []):
        sockets = getattr(srv, "sockets", None) or []
        for sock in sockets:
            try:
                bound_port = sock.getsockname()[1]
                break
            except OSError:
                continue
        if bound_port:
            break
    display_host = "localhost" if host in ("0.0.0.0", "::") else host
    return ServeHandle(
        url=f"http://{display_host}:{bound_port}",
        port=bound_port,
        _server=server,
        _task=task,
        _executor=executor,
    )


# --------------------------------------------------------- Helpers


def _spec_to_run_kwargs(spec: MantyxAgentSpec) -> dict[str, Any]:
    out: dict[str, Any] = {}
    if spec.agent_id:
        out["agent_id"] = spec.agent_id
    if spec.system_prompt:
        out["system_prompt"] = spec.system_prompt
    if spec.model_id:
        out["model_id"] = spec.model_id
    if spec.name:
        out["name"] = spec.name
    if spec.tools:
        out["tools"] = list(spec.tools)
    if spec.reasoning_level is not None:
        out["reasoning_level"] = spec.reasoning_level
    if spec.metadata:
        out["metadata"] = dict(spec.metadata)
    if spec.budgets:
        out["budgets"] = dict(spec.budgets)
    return out


def _spec_to_session_kwargs(spec: MantyxAgentSpec, context_id: str) -> dict[str, Any]:
    out = _spec_to_run_kwargs(spec)
    # Tag the session with the originating A2A context_id so it is filterable
    # in the MANTYX dashboard.
    meta = dict(out.get("metadata") or {})
    meta.setdefault("a2a_context_id", context_id)
    out["metadata"] = meta
    return out


def _build_initial_task(task_id: str, context_id: str, user_message: Message | None) -> Task:
    task = Task()
    task.id = task_id
    task.context_id = context_id
    task.status.state = TaskState.TASK_STATE_SUBMITTED
    task.status.timestamp.FromDatetime(_now_utc())
    if user_message is not None:
        task.history.append(user_message)
    return task


def _build_status_update(
    task_id: str,
    context_id: str,
    state: int,
    *,
    final: bool,
    text: str | None = None,
) -> TaskStatusUpdateEvent:
    ev = TaskStatusUpdateEvent()
    ev.task_id = task_id
    ev.context_id = context_id
    # mypy doesn't see protobuf enum values as compatible with the generated
    # `TaskState` field type, but at runtime any int that lives in the enum's
    # value range is accepted.
    ev.status.state = state  # type: ignore[assignment]
    ev.status.timestamp.FromDatetime(_now_utc())
    if text is not None:
        msg = ev.status.message
        msg.message_id = _uuid()
        msg.role = Role.ROLE_AGENT
        msg.context_id = context_id
        msg.task_id = task_id
        part = msg.parts.add()
        part.text = text
    # NOTE: A2A spec v1.0 protobuf models don't expose `final` directly on
    # `TaskStatusUpdateEvent`; the request handler infers terminal-ness from
    # the `state` (completed / canceled / failed). The `final` arg is kept
    # in the signature for cross-SDK API symmetry.
    del final  # explicit: parameter intentionally unused at the wire level.
    return ev


def _build_delta_status_update(
    task_id: str,
    context_id: str,
    delta: str,
) -> TaskStatusUpdateEvent:
    return _build_status_update(
        task_id, context_id, TaskState.TASK_STATE_WORKING, final=False, text=delta,
    )


def _now_utc() -> datetime:
    return datetime.now(timezone.utc)


def _uuid() -> str:
    return str(uuid.uuid4())
