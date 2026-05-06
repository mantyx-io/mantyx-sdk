"""Unit tests for `mantyx.a2a_server`.

Drives the executor directly with a minimal fake A2A `RequestContext` and a
`CapturingEventQueue`, asserting on the published task / status-update
sequence. End-to-end serve_agent_over_a2a smoke is exercised separately.
"""

from __future__ import annotations

from typing import Any

import pytest

from mantyx import AsyncMantyxClient, MantyxError
from mantyx.a2a_server import (
    MantyxAgentExecutor,
    MantyxAgentSpec,
    build_agent_card,
)

# Skip every test in this module when the optional `a2a-sdk` extras are
# missing.
pytest.importorskip("a2a")
pytest.importorskip("a2a.server.agent_execution")

from a2a.auth.user import UnauthenticatedUser
from a2a.server.agent_execution import RequestContext
from a2a.server.context import ServerCallContext
from a2a.server.events import EventQueue
from a2a.types import (
    Message,
    Role,
    SendMessageRequest,
    Task,
    TaskState,
    TaskStatusUpdateEvent,
)

from .conftest import MockServer, RunScript, ScriptEvent


class CapturingEventQueue(EventQueue):
    """Captures every event the executor enqueues for assertions."""

    def __init__(self) -> None:
        # Don't call super().__init__() — the base class spawns a real queue;
        # we just need `enqueue_event` to record into a list.
        self.events: list[Any] = []

    async def enqueue_event(self, event: Any) -> None:  # type: ignore[override]
        self.events.append(event)

    async def close(self) -> None:  # type: ignore[override]
        return None

    async def tap(self) -> CapturingEventQueue:  # type: ignore[override]
        return self


def _user_message(text: str, task_id: str, context_id: str) -> Message:
    msg = Message()
    msg.message_id = f"msg_{task_id}"
    msg.role = Role.ROLE_USER
    msg.task_id = task_id
    msg.context_id = context_id
    p = msg.parts.add()
    p.text = text
    return msg


def _make_request_context(text: str, task_id: str, context_id: str) -> RequestContext:
    req = SendMessageRequest()
    req.message.CopyFrom(_user_message(text, task_id, context_id))
    return RequestContext(
        call_context=ServerCallContext(user=UnauthenticatedUser()),
        request=req,
        task_id=task_id,
        context_id=context_id,
    )


def test_spec_requires_agent_id_or_system_prompt() -> None:
    with pytest.raises(MantyxError):
        MantyxAgentSpec()


def test_spec_accepts_agent_id() -> None:
    s = MantyxAgentSpec(agent_id="agent_abc")
    assert s.agent_id == "agent_abc"


def test_spec_accepts_system_prompt() -> None:
    s = MantyxAgentSpec(system_prompt="you are helpful")
    assert s.system_prompt == "you are helpful"


def test_executor_rejects_invalid_conversation_value(
    async_mantyx_client: AsyncMantyxClient,
) -> None:
    with pytest.raises(MantyxError):
        MantyxAgentExecutor(
            client=async_mantyx_client,
            agent=MantyxAgentSpec(system_prompt="x"),
            conversation="banana",
        )


@pytest.mark.asyncio
async def test_execute_stateless_publishes_task_then_completed(
    async_mantyx_client: AsyncMantyxClient, mock_server: MockServer
) -> None:
    mock_server.script_for_next_run = RunScript(
        events=[
            ScriptEvent(kind="delta", data={"text": "Hi "}),
            ScriptEvent(kind="delta", data={"text": "there"}),
            ScriptEvent(kind="result", data={"subtype": "success", "text": "Hi there"}),
        ]
    )
    exec_ = MantyxAgentExecutor(
        client=async_mantyx_client,
        agent=MantyxAgentSpec(system_prompt="you are helpful"),
        conversation="stateless",
    )
    bus = CapturingEventQueue()
    await exec_.execute(_make_request_context("Greet me", "task_1", "ctx_1"), bus)

    # Initial Task event
    assert isinstance(bus.events[0], Task)
    assert bus.events[0].id == "task_1"
    assert bus.events[0].context_id == "ctx_1"
    assert bus.events[0].status.state == TaskState.TASK_STATE_SUBMITTED

    # Working status update
    working = bus.events[1]
    assert isinstance(working, TaskStatusUpdateEvent)
    assert working.status.state == TaskState.TASK_STATE_WORKING

    # Two delta status updates
    delta_events = [
        e
        for e in bus.events
        if isinstance(e, TaskStatusUpdateEvent)
        and e.status.state == TaskState.TASK_STATE_WORKING
        and e.status.HasField("message")
    ]
    delta_texts = [e.status.message.parts[0].text for e in delta_events]
    assert delta_texts == ["Hi ", "there"]

    # Final completed status with full text
    final = bus.events[-1]
    assert isinstance(final, TaskStatusUpdateEvent)
    assert final.status.state == TaskState.TASK_STATE_COMPLETED
    assert final.status.message.parts[0].text == "Hi there"

    # Stateless mode hits /agent-runs, not /agent-sessions
    assert mock_server.last_run_create_body is not None
    assert mock_server.last_run_create_body.get("systemPrompt") == "you are helpful"
    assert mock_server.last_session_create_body is None


@pytest.mark.asyncio
async def test_execute_auto_reuses_session_per_context(
    async_mantyx_client: AsyncMantyxClient, mock_server: MockServer
) -> None:
    exec_ = MantyxAgentExecutor(
        client=async_mantyx_client,
        agent=MantyxAgentSpec(agent_id="agent_xyz"),
    )
    # Turn 1
    await exec_.execute(_make_request_context("hi", "task_1", "ctx_one"), CapturingEventQueue())
    assert mock_server.last_session_create_body is not None
    assert mock_server.last_session_create_body.get("agentId") == "agent_xyz"
    meta = mock_server.last_session_create_body.get("metadata") or {}
    assert meta.get("a2a_context_id") == "ctx_one"

    mock_server.last_session_create_body = None

    # Turn 2 with the same context_id reuses the session.
    await exec_.execute(
        _make_request_context("follow-up", "task_2", "ctx_one"), CapturingEventQueue()
    )
    assert mock_server.last_session_create_body is None
    assert mock_server.last_session_message_body is not None
    assert mock_server.last_session_message_body.get("prompt") == "follow-up"

    await exec_.aclose()


@pytest.mark.asyncio
async def test_execute_publishes_failed_on_run_error(
    async_mantyx_client: AsyncMantyxClient, mock_server: MockServer
) -> None:
    # Force the run to terminate with a non-success subtype.
    mock_server.script_for_next_run = RunScript(
        events=[ScriptEvent(kind="result", data={"subtype": "error_internal", "text": "boom"})]
    )
    exec_ = MantyxAgentExecutor(
        client=async_mantyx_client,
        agent=MantyxAgentSpec(system_prompt="you are helpful"),
        conversation="stateless",
    )
    bus = CapturingEventQueue()
    await exec_.execute(_make_request_context("hi", "task_err", "ctx_err"), bus)

    final = bus.events[-1]
    assert isinstance(final, TaskStatusUpdateEvent)
    assert final.status.state == TaskState.TASK_STATE_FAILED


def test_build_agent_card_round_trip() -> None:
    card = build_agent_card(
        name="Acme Bot",
        description="test",
        version="1.0.0",
        public_url="https://example.com/a2a",
        skills=[{"id": "lookup", "name": "Lookup", "description": "Look stuff up", "tags": ["q"]}],
    )
    assert card.name == "Acme Bot"
    assert card.version == "1.0.0"
    assert list(card.default_input_modes) == ["text"]
    assert list(card.default_output_modes) == ["text"]
    assert card.capabilities.streaming is True
    assert len(card.skills) == 1
    assert card.skills[0].id == "lookup"
    assert list(card.skills[0].tags) == ["q"]
    iface = next(iter(card.supported_interfaces))
    assert iface.url == "https://example.com/a2a"
    assert iface.protocol_binding == "JSONRPC"
