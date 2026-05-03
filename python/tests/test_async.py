"""Tests for the async client."""

from __future__ import annotations

import pytest
from pydantic import BaseModel

from mantyx import AsyncMantyxClient, define_local_tool

from .conftest import MockServer, RunScript, ScriptEvent


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
