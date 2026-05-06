"""Tests for the URL-only ``a2a_local`` / ``mcp_local`` API.

The mock server in ``conftest.py`` exposes:

  * ``GET  /a2a/agent-card.json`` — returns an A2A Agent Card so
    ``define_local_a2a`` can resolve end-to-end.
  * ``POST /a2a/rpc``             — answers JSON-RPC ``message/send`` so
    the SDK's dispatch path can post to a real endpoint.

For ``mcp_local`` we don't spin up a real MCP server in unit tests —
instead we seed the ref's internal ``_resolved`` field with a fake
:class:`_ResolvedMcpServer` so the SDK exercises serialisation +
dispatch without depending on the official MCP transport.
"""

from __future__ import annotations

import pytest
from pydantic import BaseModel

from mantyx import (
    MantyxClient,
    MantyxParseError,
    define_local_a2a,
    define_local_mcp,
    define_local_tool,
    is_local_a2a_tool,
    is_local_mcp_server,
    mantyx_a2a,
    mantyx_mcp,
    parse_run_output,
)
from mantyx.tools import (
    LocalMcpServer,
    _ResolvedMcpServer,
    normalize_output_schema,
    normalize_reasoning_level,
)

from .conftest import MockServer, RunScript, ScriptEvent


def _seed_mcp_resolution(
    ref: LocalMcpServer,
    *,
    server_info: dict[str, object],
    tools: list[dict[str, object]],
    on_call: object = None,
) -> list[dict[str, object]]:
    """Seed an `mcp_local` ref with a fake resolved snapshot, bypassing the
    real MCP transport. The returned list captures every dispatch."""

    calls: list[dict[str, object]] = []

    async def _call_async(name: str, arguments: dict[str, object]) -> str:
        calls.append({"name": name, "arguments": arguments})
        if callable(on_call):
            return str(on_call(name, arguments))
        return f"ok:{name}"

    def _call_sync(name: str, arguments: dict[str, object]) -> str:
        calls.append({"name": name, "arguments": arguments})
        if callable(on_call):
            return str(on_call(name, arguments))
        return f"ok:{name}"

    async def _aclose() -> None:
        return None

    def _close_sync() -> None:
        return None

    ref._resolved = _ResolvedMcpServer(
        server_info=server_info,
        tools=tools,
        call_async=_call_async,
        aclose=_aclose,
        call_sync=_call_sync,
        close_sync=_close_sync,
    )
    return calls


# --------------------------------------------------------------- Serialization


def test_mantyx_a2a_serialization(mantyx_client: MantyxClient, mock_server: MockServer) -> None:
    mantyx_client.run_agent(
        system_prompt="x",
        prompt="y",
        tools=[
            mantyx_a2a(
                name="research_agent",
                agent_card_url="https://peer.example/.well-known/agent-card.json",
                description="Delegate deep research.",
                headers={"Authorization": "Bearer xyz"},
                context_id="ctx_abc",
            )
        ],
    )
    body = mock_server.last_run_create_body
    assert body is not None
    assert body["tools"] == [
        {
            "kind": "a2a",
            "name": "research_agent",
            "agentCardUrl": "https://peer.example/.well-known/agent-card.json",
            "description": "Delegate deep research.",
            "headers": {"Authorization": "Bearer xyz"},
            "contextId": "ctx_abc",
        }
    ]


def test_mantyx_mcp_serialization(mantyx_client: MantyxClient, mock_server: MockServer) -> None:
    mantyx_client.run_agent(
        system_prompt="x",
        prompt="y",
        tools=[
            mantyx_mcp(
                name="github",
                url="https://api.example.com/mcp",
                headers={"Authorization": "Bearer t"},
                tool_filter=["create_issue"],
            )
        ],
    )
    body = mock_server.last_run_create_body
    assert body is not None
    assert body["tools"] == [
        {
            "kind": "mcp",
            "name": "github",
            "url": "https://api.example.com/mcp",
            "headers": {"Authorization": "Bearer t"},
            "toolFilter": ["create_issue"],
        }
    ]


def test_define_local_a2a_auto_resolves_card_and_ships_it_on_the_wire(
    mantyx_client: MantyxClient, mock_server: MockServer
) -> None:
    """`define_local_a2a` is URL-only — the SDK fetches the card and ships
    the resolved content on the wire."""
    mock_server.a2a_agent_card = {
        "name": "Acme HR",
        "description": "Answers HR policy questions.",
        "url": "http://mock/a2a/rpc",
        "protocolVersion": "0.3.0",
        "skills": [{"id": "pto_lookup", "name": "PTO lookup"}],
    }
    tool = define_local_a2a(
        name="intranet_hr",
        agent_card_url="http://mock/a2a/agent-card.json",
        headers={"Authorization": "Bearer intra-token"},
    )
    assert is_local_a2a_tool(tool)
    mantyx_client.run_agent(system_prompt="x", prompt="y", tools=[tool])

    body = mock_server.last_run_create_body
    assert body is not None
    # Per `docs/wire-protocol.md` §3.1 — `kind: "a2a_local"` ships the
    # resolved Agent Card; the user only supplied a URL.
    assert body["tools"] == [
        {
            "kind": "a2a_local",
            "name": "intranet_hr",
            "agentCard": mock_server.a2a_agent_card,
        }
    ]
    assert "agentCardUrl" not in body["tools"][0]


def test_define_local_a2a_requires_agent_card_url() -> None:
    with pytest.raises(ValueError, match=r"agent_card_url"):
        define_local_a2a(name="x", agent_card_url="")


def test_define_local_a2a_rejects_card_missing_name(
    mantyx_client: MantyxClient, mock_server: MockServer
) -> None:
    """If the card endpoint returns JSON without a `name`, resolution fails
    cleanly before the SDK submits the spec."""
    mock_server.a2a_agent_card = {"description": "no name"}  # type: ignore[assignment]
    tool = define_local_a2a(name="x", agent_card_url="http://mock/a2a/agent-card.json")
    with pytest.raises(ValueError, match=r"name"):
        mantyx_client.run_agent(system_prompt="x", prompt="y", tools=[tool])


def test_define_local_mcp_ships_resolved_catalog(
    mantyx_client: MantyxClient, mock_server: MockServer
) -> None:
    mcp = define_local_mcp(name="fs", url="http://localhost:9999/mcp")
    assert is_local_mcp_server(mcp)
    _seed_mcp_resolution(
        mcp,
        server_info={"name": "mcp-server-filesystem", "version": "0.4.1"},
        tools=[
            {
                "name": "read_file",
                "description": "Read a file.",
                "inputSchema": {"type": "object", "properties": {"path": {"type": "string"}}},
                "annotations": {"readOnlyHint": True},
            }
        ],
    )
    mantyx_client.run_agent(system_prompt="x", prompt="y", tools=[mcp])

    body = mock_server.last_run_create_body
    assert body is not None
    assert body["tools"] == [
        {
            "kind": "mcp_local",
            "name": "fs",
            "serverInfo": {"name": "mcp-server-filesystem", "version": "0.4.1"},
            "tools": [
                {
                    # SDK auto-prefixes `read_file` → wire `fs_read_file`,
                    # mirroring how MANTYX prefixes for `kind: "mcp"`.
                    "name": "fs_read_file",
                    "inputSchema": {"type": "object", "properties": {"path": {"type": "string"}}},
                    "description": "Read a file.",
                    "annotations": {"readOnlyHint": True},
                }
            ],
        }
    ]


def test_define_local_mcp_rejects_both_or_neither_transport() -> None:
    with pytest.raises(ValueError):
        define_local_mcp(name="fs", url="http://x/mcp", command="mcp-server-fs")
    with pytest.raises(ValueError):
        define_local_mcp(name="fs")


def test_invalid_tool_names_are_rejected() -> None:
    with pytest.raises(ValueError):
        mantyx_a2a(name="bad name", agent_card_url="https://x")
    with pytest.raises(ValueError):
        mantyx_mcp(name="not!ok", url="https://x")
    with pytest.raises(ValueError):
        define_local_a2a(name="white space", agent_card_url="https://x")


# ------------------------------------------------------------- reasoning_level


def test_reasoning_level_string_is_forwarded(
    mantyx_client: MantyxClient, mock_server: MockServer
) -> None:
    mantyx_client.run_agent(system_prompt="x", prompt="y", reasoning_level="medium")
    body = mock_server.last_run_create_body
    assert body is not None
    assert body["reasoningLevel"] == "medium"


def test_reasoning_level_int_is_forwarded(
    mantyx_client: MantyxClient, mock_server: MockServer
) -> None:
    mantyx_client.run_agent(system_prompt="x", prompt="y", reasoning_level=42)
    body = mock_server.last_run_create_body
    assert body is not None
    assert body["reasoningLevel"] == 42


def test_reasoning_level_none_is_omitted(
    mantyx_client: MantyxClient, mock_server: MockServer
) -> None:
    mantyx_client.run_agent(system_prompt="x", prompt="y")
    body = mock_server.last_run_create_body
    assert body is not None
    assert "reasoningLevel" not in body


def test_reasoning_level_validates_range() -> None:
    assert normalize_reasoning_level("off") == "off"
    assert normalize_reasoning_level(0) == 0
    assert normalize_reasoning_level(100) == 100
    with pytest.raises(ValueError):
        normalize_reasoning_level(-1)
    with pytest.raises(ValueError):
        normalize_reasoning_level(101)
    with pytest.raises(ValueError):
        normalize_reasoning_level("MEDIUM")  # type: ignore[arg-type]
    with pytest.raises(ValueError):
        normalize_reasoning_level(True)  # type: ignore[arg-type]


def test_reasoning_level_in_session_message(
    mantyx_client: MantyxClient, mock_server: MockServer
) -> None:
    session = mantyx_client.create_session(system_prompt="x")
    session.send("hi", reasoning_level="high")
    body = mock_server.last_session_message_body
    assert body is not None
    assert body["reasoningLevel"] == "high"


# --------------------------------------------------------------- output_schema


_WEATHER_SCHEMA: dict[str, object] = {
    "type": "object",
    "properties": {
        "city": {"type": "string"},
        "temperature_c": {"type": "number"},
    },
    "required": ["city", "temperature_c"],
    "additionalProperties": False,
}


def test_output_schema_forwarded_on_run(
    mantyx_client: MantyxClient, mock_server: MockServer
) -> None:
    mantyx_client.run_agent(
        system_prompt="x",
        prompt="y",
        output_schema={"name": "weather_report", "schema": _WEATHER_SCHEMA},
    )
    body = mock_server.last_run_create_body
    assert body is not None
    assert body["outputSchema"] == {
        "name": "weather_report",
        "schema": _WEATHER_SCHEMA,
    }


def test_output_schema_omitted_when_unset(
    mantyx_client: MantyxClient, mock_server: MockServer
) -> None:
    mantyx_client.run_agent(system_prompt="x", prompt="y")
    body = mock_server.last_run_create_body
    assert body is not None
    assert "outputSchema" not in body


def test_output_schema_validation() -> None:
    assert normalize_output_schema(None) is None
    assert normalize_output_schema({"schema": _WEATHER_SCHEMA}) == {"schema": _WEATHER_SCHEMA}
    assert normalize_output_schema({"name": "ok-name_1", "schema": _WEATHER_SCHEMA}) == {
        "name": "ok-name_1",
        "schema": _WEATHER_SCHEMA,
    }
    with pytest.raises(ValueError):
        normalize_output_schema({"name": "bad name!", "schema": _WEATHER_SCHEMA})
    with pytest.raises(ValueError):
        normalize_output_schema({"schema": []})  # type: ignore[arg-type]
    with pytest.raises(ValueError):
        normalize_output_schema({"schema": None})  # type: ignore[arg-type]
    with pytest.raises(ValueError):
        normalize_output_schema({"name": "ok"})  # type: ignore[typeddict-item]
    with pytest.raises(ValueError):
        normalize_output_schema("not a mapping")  # type: ignore[arg-type]


def test_output_schema_size_limit_is_enforced() -> None:
    huge: dict[str, object] = {"type": "object", "properties": {}}
    props: dict[str, object] = {}
    for i in range(4000):
        props[f"f_{i}"] = {"type": "string", "description": "x" * 8}
    huge["properties"] = props
    with pytest.raises(ValueError, match="32 KB"):
        normalize_output_schema({"schema": huge})


def test_output_schema_in_session_message(
    mantyx_client: MantyxClient, mock_server: MockServer
) -> None:
    session = mantyx_client.create_session(
        system_prompt="x",
        output_schema={"schema": _WEATHER_SCHEMA},
    )
    body = mock_server.last_session_create_body
    assert body is not None
    assert body["outputSchema"] == {"schema": _WEATHER_SCHEMA}

    override = {
        "type": "object",
        "properties": {"ok": {"type": "boolean"}},
        "required": ["ok"],
    }
    session.send("hi", output_schema={"name": "ack", "schema": override})
    msg_body = mock_server.last_session_message_body
    assert msg_body is not None
    assert msg_body["outputSchema"] == {"name": "ack", "schema": override}


# --------------------------------------------------------------- parse_run_output


class _Weather(BaseModel):
    city: str
    temperature_c: float


def test_parse_run_output_returns_dict_when_no_validator(
    mantyx_client: MantyxClient, mock_server: MockServer
) -> None:
    mock_server.script_for_next_run = RunScript(
        events=[
            ScriptEvent(
                kind="result",
                data={
                    "subtype": "success",
                    "text": '{"city":"SF","temperature_c":17.0}',
                },
            )
        ]
    )
    result = mantyx_client.run_agent(system_prompt="x", prompt="y")
    parsed = parse_run_output(result)
    assert parsed == {"city": "SF", "temperature_c": 17.0}


def test_parse_run_output_runs_pydantic_validator(
    mantyx_client: MantyxClient, mock_server: MockServer
) -> None:
    mock_server.script_for_next_run = RunScript(
        events=[
            ScriptEvent(
                kind="result",
                data={
                    "subtype": "success",
                    "text": '{"city":"SF","temperature_c":17.0}',
                },
            )
        ]
    )
    result = mantyx_client.run_agent(system_prompt="x", prompt="y")
    report = parse_run_output(result, _Weather.model_validate)
    assert isinstance(report, _Weather)
    assert report.city == "SF"
    assert report.temperature_c == 17.0


def test_parse_run_output_raises_on_non_json_text(
    mantyx_client: MantyxClient, mock_server: MockServer
) -> None:
    mock_server.script_for_next_run = RunScript(
        events=[
            ScriptEvent(
                kind="result",
                data={"subtype": "success", "text": "I refuse to answer in JSON."},
            )
        ]
    )
    result = mantyx_client.run_agent(system_prompt="x", prompt="y")
    with pytest.raises(MantyxParseError) as info:
        parse_run_output(result)
    assert info.value.text == "I refuse to answer in JSON."


def test_parse_run_output_raises_when_validator_fails(
    mantyx_client: MantyxClient, mock_server: MockServer
) -> None:
    mock_server.script_for_next_run = RunScript(
        events=[
            ScriptEvent(
                kind="result",
                data={"subtype": "success", "text": '{"city": 42}'},
            )
        ]
    )
    result = mantyx_client.run_agent(system_prompt="x", prompt="y")
    with pytest.raises(MantyxParseError):
        parse_run_output(result, _Weather.model_validate)


# -------------------------------------------------- local_tool_call dispatch


def test_local_tool_call_dispatch_posts_a2a_message_send(
    mantyx_client: MantyxClient, mock_server: MockServer
) -> None:
    mock_server.a2a_reply_text = "PTO resets on Jan 1."
    tool = define_local_a2a(
        name="intranet_hr",
        agent_card_url="http://mock/a2a/agent-card.json",
        headers={"Authorization": "Bearer intra-token"},
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

    result = mantyx_client.run_agent(system_prompt="x", prompt="y", tools=[tool])

    assert result.text == "done"
    # The SDK posted JSON-RPC `message/send` to the card's URL with the
    # model's `message` argument verbatim, and forwarded the auth header.
    a2a_req = mock_server.last_a2a_request
    assert a2a_req is not None
    assert a2a_req["method"] == "message/send"
    assert a2a_req["message"] == "When does PTO reset?"
    assert a2a_req["headers"]["authorization"] == "Bearer intra-token"
    body = mock_server.last_tool_result_body
    assert body is not None
    assert body["toolUseId"] == "tu_1"
    assert body["result"] == "PTO resets on Jan 1."


def test_local_tool_call_dispatch_to_mcp_local(
    mantyx_client: MantyxClient, mock_server: MockServer
) -> None:
    mcp = define_local_mcp(name="fs", url="http://localhost:9999/mcp")
    calls = _seed_mcp_resolution(
        mcp,
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
                    "args": {"path": "/etc/hostname"},
                },
                wait_for_result=True,
            ),
            ScriptEvent(kind="result", data={"subtype": "success", "text": "done"}),
        ]
    )

    result = mantyx_client.run_agent(system_prompt="x", prompt="y", tools=[mcp])
    assert result.text == "done"
    # The SDK strips the `<server>_` prefix before forwarding to MCP
    # `tools/call` (the upstream server uses the bare name).
    assert calls == [{"name": "read_file", "arguments": {"path": "/etc/hostname"}}]
    body = mock_server.last_tool_result_body
    assert body is not None
    assert body["result"] == "contents:/etc/hostname"


def test_local_tool_call_default_kind_dispatches_generic_local(
    mantyx_client: MantyxClient, mock_server: MockServer
) -> None:
    """When ``kind`` is omitted the SDK falls back to the generic ``local``
    registry — every pre-A2A/MCP server emits this shape."""

    class Args(BaseModel):
        v: int

    def handle(args: Args) -> str:
        return f"v={args.v}"

    tool = define_local_tool(name="echo", parameters=Args, execute=handle)

    mock_server.script_for_next_run = RunScript(
        events=[
            ScriptEvent(
                kind="local_tool_call",
                data={"toolUseId": "tu_1", "name": "echo", "args": {"v": 7}},
                wait_for_result=True,
            ),
            ScriptEvent(kind="result", data={"subtype": "success", "text": "done"}),
        ]
    )

    mantyx_client.run_agent(system_prompt="x", prompt="y", tools=[tool])
    body = mock_server.last_tool_result_body
    assert body is not None
    assert body["result"] == "v=7"


def test_local_tool_call_missing_a2a_handler_reports_error(
    mantyx_client: MantyxClient, mock_server: MockServer
) -> None:
    mock_server.script_for_next_run = RunScript(
        events=[
            ScriptEvent(
                kind="local_tool_call",
                data={
                    "toolUseId": "tu_1",
                    "name": "ghost",
                    "kind": "a2a_local",
                    "args": {"message": "hi"},
                },
                wait_for_result=True,
            ),
            ScriptEvent(kind="result", data={"subtype": "success", "text": "done"}),
        ]
    )

    mantyx_client.run_agent(system_prompt="x", prompt="y")
    body = mock_server.last_tool_result_body
    assert body is not None
    assert "error" in body
    assert "ghost" in body["error"]


def test_local_tool_call_missing_mcp_handler_reports_error(
    mantyx_client: MantyxClient, mock_server: MockServer
) -> None:
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
                    "args": {"path": "/x"},
                },
                wait_for_result=True,
            ),
            ScriptEvent(kind="result", data={"subtype": "success", "text": "done"}),
        ]
    )

    mantyx_client.run_agent(system_prompt="x", prompt="y")
    body = mock_server.last_tool_result_body
    assert body is not None
    assert "error" in body
    assert "fs" in body["error"]
