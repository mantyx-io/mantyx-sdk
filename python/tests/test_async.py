"""Tests for the async client (URL-only `define_local_a2a` + `define_local_mcp`)."""

from __future__ import annotations

import pytest
from pydantic import BaseModel

from mantyx import (
    AsyncMantyxClient,
    define_local_a2a,
    define_local_mcp,
    define_local_tool,
)
from mantyx.tools import LocalMcpServer, _ResolvedMcpServer

from .conftest import MockServer, RunScript, ScriptEvent


def _seed_async_mcp(
    ref: LocalMcpServer,
    *,
    server_info: dict[str, object],
    tools: list[dict[str, object]],
    on_call: object = None,
) -> list[dict[str, object]]:
    """Seed a `mcp_local` ref with a fake async-only resolution."""
    calls: list[dict[str, object]] = []

    async def _call_async(name: str, arguments: dict[str, object]) -> str:
        calls.append({"name": name, "arguments": arguments})
        if callable(on_call):
            return str(on_call(name, arguments))
        return f"ok:{name}"

    async def _aclose() -> None:
        return None

    ref._resolved = _ResolvedMcpServer(
        server_info=server_info,
        tools=tools,
        call_async=_call_async,
        aclose=_aclose,
    )
    return calls


@pytest.mark.asyncio
async def test_async_run_agent(
    async_mantyx_client: AsyncMantyxClient, mock_server: MockServer
) -> None:
    mock_server.script_for_next_run = RunScript(
        events=[
            ScriptEvent(kind="delta", data={"text": "Hi "}),
            ScriptEvent(kind="delta", data={"text": "there"}),
            ScriptEvent(kind="result", data={"subtype": "success", "text": "Hi there"}),
        ]
    )
    deltas: list[str] = []

    async def on_delta(d: str) -> None:
        deltas.append(d)

    result = await async_mantyx_client.run_agent(
        system_prompt="x",
        prompt="say hi",
        on_assistant_delta=on_delta,
    )
    assert result.text == "Hi there"
    assert deltas == ["Hi ", "there"]


@pytest.mark.asyncio
async def test_async_stream_agent(
    async_mantyx_client: AsyncMantyxClient, mock_server: MockServer
) -> None:
    mock_server.script_for_next_run = RunScript(
        events=[
            ScriptEvent(kind="delta", data={"text": "ok"}),
            ScriptEvent(kind="result", data={"subtype": "success", "text": "ok"}),
        ]
    )
    types = []
    async for ev in async_mantyx_client.stream_agent(system_prompt="x", prompt="y"):
        types.append(ev.type)
    assert "assistant_delta" in types
    assert types[-1] == "result"


@pytest.mark.asyncio
async def test_async_local_tool(
    async_mantyx_client: AsyncMantyxClient, mock_server: MockServer
) -> None:
    class Args(BaseModel):
        n: int

    async def execute(args: Args) -> str:
        return str(args.n * 2)

    tool = define_local_tool(name="double", parameters=Args, execute=execute)

    mock_server.script_for_next_run = RunScript(
        events=[
            ScriptEvent(
                kind="local_tool_call",
                data={"toolUseId": "tu_x", "name": "double", "args": {"n": 21}},
                wait_for_result=True,
            ),
            ScriptEvent(kind="result", data={"subtype": "success", "text": "42"}),
        ]
    )
    result = await async_mantyx_client.run_agent(
        system_prompt="x", prompt="double 21", tools=[tool]
    )
    assert result.text == "42"
    assert mock_server.last_tool_result_body is not None
    assert mock_server.last_tool_result_body["result"] == "42"


@pytest.mark.asyncio
async def test_async_local_a2a_resolves_card_and_dispatches(
    async_mantyx_client: AsyncMantyxClient, mock_server: MockServer
) -> None:
    """`define_local_a2a` is URL-only — the async client fetches the card,
    ships it on the wire, and POSTs `message/send` on dispatch."""
    mock_server.a2a_reply_text = "PTO resets on Jan 1."
    tool = define_local_a2a(
        name="intranet_hr",
        agent_card_url="http://mock/a2a/agent-card.json",
        headers={"Authorization": "Bearer intra"},
    )

    mock_server.script_for_next_run = RunScript(
        events=[
            ScriptEvent(
                kind="local_tool_call",
                data={
                    "toolUseId": "tu_1",
                    "name": "intranet_hr",
                    "kind": "a2a_local",
                    "args": {"message": "When does PTO reset?"},
                    "agentCard": mock_server.a2a_agent_card,
                },
                wait_for_result=True,
            ),
            ScriptEvent(kind="result", data={"subtype": "success", "text": "done"}),
        ]
    )

    result = await async_mantyx_client.run_agent(system_prompt="x", prompt="y", tools=[tool])
    assert result.text == "done"
    a2a_req = mock_server.last_a2a_request
    assert a2a_req is not None
    assert a2a_req["method"] == "message/send"
    assert a2a_req["message"] == "When does PTO reset?"
    assert mock_server.last_tool_result_body is not None
    assert mock_server.last_tool_result_body["result"] == "PTO resets on Jan 1."

    # Verify the resolved card was shipped on the wire as part of the spec.
    create_body = mock_server.last_run_create_body
    assert create_body is not None
    assert create_body["tools"][0]["agentCard"] == mock_server.a2a_agent_card


@pytest.mark.asyncio
async def test_async_local_mcp_dispatch(
    async_mantyx_client: AsyncMantyxClient, mock_server: MockServer
) -> None:
    server = define_local_mcp(name="fs", url="http://localhost:9999/mcp")
    calls = _seed_async_mcp(
        server,
        server_info={"name": "mcp-server-filesystem"},
        tools=[{"name": "read_file", "inputSchema": {"type": "object"}}],
        on_call=lambda name, args: f"contents:{args['path']}",
    )

    mock_server.script_for_next_run = RunScript(
        events=[
            ScriptEvent(
                kind="local_tool_call",
                data={
                    "toolUseId": "tu_1",
                    "name": "fs_read_file",
                    "kind": "mcp_local",
                    "mcpServer": "fs",
                    "mcpToolName": "fs_read_file",
                    "args": {"path": "/etc/host"},
                },
                wait_for_result=True,
            ),
            ScriptEvent(kind="result", data={"subtype": "success", "text": "done"}),
        ]
    )

    await async_mantyx_client.run_agent(system_prompt="x", prompt="y", tools=[server])
    body = mock_server.last_tool_result_body
    assert body is not None
    assert body["result"] == "contents:/etc/host"
    # Strip the wire prefix before forwarding to upstream `tools/call`.
    assert calls == [{"name": "read_file", "arguments": {"path": "/etc/host"}}]


@pytest.mark.asyncio
async def test_async_reasoning_level_forwarded(
    async_mantyx_client: AsyncMantyxClient, mock_server: MockServer
) -> None:
    await async_mantyx_client.run_agent(system_prompt="x", prompt="y", reasoning_level="medium")
    body = mock_server.last_run_create_body
    assert body is not None
    assert body["reasoningLevel"] == "medium"
