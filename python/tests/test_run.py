"""Tests for ``run_agent`` and ``stream_agent``."""

from __future__ import annotations

import pytest
from pydantic import BaseModel

from mantyx import (
    MantyxClient,
    MantyxRunError,
    define_local_tool,
    mantyx_plugin_tool,
    mantyx_tool,
)

from .conftest import MockServer, RunScript, ScriptEvent


def test_run_agent_success(mantyx_client: MantyxClient, mock_server: MockServer) -> None:
    mock_server.script_for_next_run = RunScript(
        events=[
            ScriptEvent(kind="delta", data={"text": "hello "}),
            ScriptEvent(kind="delta", data={"text": "world"}),
            ScriptEvent(kind="result", data={"subtype": "success", "text": "hello world"}),
        ]
    )

    deltas: list[str] = []
    result = mantyx_client.run_agent(
        system_prompt="You are a friendly assistant.",
        prompt="Say hello.",
        on_assistant_delta=lambda d: deltas.append(d),
    )
    assert result.text == "hello world"
    assert deltas == ["hello ", "world"]
    assert result.run_id.startswith("run_")
    assert any(ev.type == "result" for ev in result.events)


def test_run_agent_with_local_tool(mantyx_client: MantyxClient, mock_server: MockServer) -> None:
    class Args(BaseModel):
        path: str

    captured: list[Args] = []

    def execute(args: Args) -> str:
        captured.append(args)
        return f"contents-of:{args.path}"

    tool = define_local_tool(name="read_file", parameters=Args, execute=execute)

    mock_server.script_for_next_run = RunScript(
        events=[
            ScriptEvent(
                kind="local_tool_call",
                data={"toolUseId": "tu_1", "name": "read_file", "args": {"path": "/etc/host"}},
                wait_for_result=True,
            ),
            ScriptEvent(kind="result", data={"subtype": "success", "text": "done"}),
        ]
    )

    result = mantyx_client.run_agent(
        system_prompt="You are an assistant.",
        prompt="Read /etc/host.",
        tools=[tool],
    )
    assert result.text == "done"
    assert captured[0].path == "/etc/host"
    assert mock_server.last_tool_result_body is not None
    assert mock_server.last_tool_result_body["toolUseId"] == "tu_1"
    assert mock_server.last_tool_result_body["result"] == "contents-of:/etc/host"


def test_run_agent_error_terminates(mantyx_client: MantyxClient, mock_server: MockServer) -> None:
    mock_server.script_for_next_run = RunScript(
        events=[
            ScriptEvent(
                kind="result",
                data={"subtype": "error_max_tool_turns", "error": "too many tool turns"},
            )
        ]
    )
    with pytest.raises(MantyxRunError) as exc:
        mantyx_client.run_agent(system_prompt="...", prompt="...")
    assert exc.value.subtype == "error_max_tool_turns"


def test_run_agent_error_event_carries_triage_attributes(
    mantyx_client: MantyxClient, mock_server: MockServer
) -> None:
    # Mirrors the "truncation salvage" path from docs/agent-runs-protocol.md
    # §7: the engine emits an `assistant_message` with the partial text and
    # a `finishReason`, then a terminal `error` event with `errorClass:
    # "truncation"` and the same bytes on `partialText`.
    mock_server.script_for_next_run = RunScript(
        events=[
            ScriptEvent(
                kind="assistant_message",
                data={
                    "text": '{"answer":"hello',
                    "turn": 0,
                    "finishReason": "max_tokens",
                },
            ),
            ScriptEvent(
                kind="error",
                data={
                    "error": "Model output was truncated (stop_reason=max_tokens).",
                    "code": "truncation",
                    "errorClass": "truncation",
                    "finishReason": "max_tokens",
                    "partialText": '{"answer":"hello',
                    "retryable": False,
                },
            ),
        ]
    )
    with pytest.raises(MantyxRunError) as exc:
        mantyx_client.run_agent(system_prompt="...", prompt="...")
    err = exc.value
    assert err.subtype == "truncation"
    assert err.code == "truncation"
    assert err.error_class == "truncation"
    assert err.finish_reason == "max_tokens"
    assert err.partial_text == '{"answer":"hello'
    assert err.retryable is False
    assert "truncated" in err.message


def test_run_agent_error_event_falls_back_to_code(
    mantyx_client: MantyxClient, mock_server: MockServer
) -> None:
    # Older runners may not yet emit `errorClass`; the SDK should still
    # surface the legacy `code` value on `MantyxRunError.subtype`.
    mock_server.script_for_next_run = RunScript(
        events=[
            ScriptEvent(
                kind="error",
                data={"error": "boom", "code": "worker_error"},
            )
        ]
    )
    with pytest.raises(MantyxRunError) as exc:
        mantyx_client.run_agent(system_prompt="...", prompt="...")
    err = exc.value
    assert err.subtype == "worker_error"
    assert err.error_class is None
    assert err.finish_reason is None
    assert err.partial_text is None
    assert err.retryable is None


def test_run_agent_assistant_message_surfaces_triage_fields(
    mantyx_client: MantyxClient, mock_server: MockServer
) -> None:
    mock_server.script_for_next_run = RunScript(
        events=[
            ScriptEvent(
                kind="assistant_message",
                data={
                    "text": "calling search",
                    "turn": 0,
                    "finishReason": "tool_use",
                    "toolCalls": [{"id": "call_a", "name": "search", "input": {"q": "hi"}}],
                },
            ),
            ScriptEvent(kind="result", data={"subtype": "success", "text": "done"}),
        ]
    )
    result = mantyx_client.run_agent(system_prompt="...", prompt="...")
    msg_events = [ev for ev in result.events if ev.type == "assistant_message"]
    assert len(msg_events) == 1
    msg = msg_events[0]
    assert msg.data["text"] == "calling search"
    assert msg.data["turn"] == 0
    assert msg.data["finishReason"] == "tool_use"
    assert msg.data["toolCalls"] == [{"id": "call_a", "name": "search", "input": {"q": "hi"}}]


def test_run_agent_surfaces_cost_attribution_from_result(
    mantyx_client: MantyxClient, mock_server: MockServer
) -> None:
    # Cost-attribution triple from `docs/agent-runs-protocol.md` §7.1:
    # the successful terminal `result` event carries `tokens` / `turns`
    # / `model`, which the SDK lifts onto `RunResult`.
    mock_server.script_for_next_run = RunScript(
        events=[
            ScriptEvent(
                kind="result",
                data={
                    "subtype": "success",
                    "text": "Hello world",
                    "tokens": {
                        "inputTokens": 1283,
                        "cachedTokens": 512,
                        "reasoningTokens": 96,
                        "outputTokens": 240,
                    },
                    "turns": 3,
                    "model": {
                        "id": "platform:demo",
                        "provider": "openai",
                        "vendorModelId": "gpt-test",
                        "reasoningEffort": "low",
                    },
                },
            )
        ]
    )
    result = mantyx_client.run_agent(system_prompt="x", prompt="y")
    assert result.tokens is not None
    assert result.tokens.input_tokens == 1283
    assert result.tokens.cached_tokens == 512
    assert result.tokens.reasoning_tokens == 96
    assert result.tokens.output_tokens == 240
    assert result.turns == 3
    assert result.model is not None
    assert result.model.id == "platform:demo"
    assert result.model.provider == "openai"
    assert result.model.vendor_model_id == "gpt-test"
    assert result.model.reasoning_effort == "low"


def test_run_agent_legacy_server_omits_cost_attribution(
    mantyx_client: MantyxClient, mock_server: MockServer
) -> None:
    # Older servers omit `tokens` / `turns` / `model` entirely. The SDK
    # leaves the fields at ``None`` so callers can detect "no usage
    # data" via `result.model is None`.
    mock_server.script_for_next_run = RunScript(
        events=[ScriptEvent(kind="result", data={"subtype": "success", "text": "ok"})]
    )
    result = mantyx_client.run_agent(system_prompt="x", prompt="y")
    assert result.tokens is None
    assert result.turns is None
    assert result.model is None


def test_run_agent_error_event_carries_cost_attribution(
    mantyx_client: MantyxClient, mock_server: MockServer
) -> None:
    # Failed runs on MANTYX ≥ 2026-09 also carry the cost-attribution
    # triple (the failing model call's usage is included). See §7.1.
    mock_server.script_for_next_run = RunScript(
        events=[
            ScriptEvent(
                kind="error",
                data={
                    "error": "Model output was truncated (stop_reason=max_tokens).",
                    "errorClass": "truncation",
                    "finishReason": "max_tokens",
                    "partialText": '{"answer":"hello',
                    "retryable": False,
                    "tokens": {
                        "inputTokens": 8190,
                        "cachedTokens": 0,
                        "reasoningTokens": 0,
                        "outputTokens": 1024,
                    },
                    "turns": 1,
                    "model": {
                        "id": "provider:cmf",
                        "provider": "google",
                        "vendorModelId": "gemini-2.5-pro",
                    },
                },
            )
        ]
    )
    with pytest.raises(MantyxRunError) as exc:
        mantyx_client.run_agent(system_prompt="x", prompt="y")
    err = exc.value
    assert err.tokens is not None
    assert err.tokens.input_tokens == 8190
    assert err.tokens.output_tokens == 1024
    assert err.turns == 1
    assert err.model is not None
    assert err.model.provider == "google"
    assert err.model.vendor_model_id == "gemini-2.5-pro"


def test_run_agent_clamps_malformed_token_buckets(
    mantyx_client: MantyxClient, mock_server: MockServer
) -> None:
    # A misbehaving engine that ships negatives / NaN / strings should
    # not poison the JSON snapshot — the SDK clamps every bucket to
    # non-negative integers (mirroring server-side behaviour).
    mock_server.script_for_next_run = RunScript(
        events=[
            ScriptEvent(
                kind="result",
                data={
                    "subtype": "success",
                    "text": "ok",
                    "tokens": {
                        "inputTokens": -10,
                        "cachedTokens": "not a number",
                        "outputTokens": 12.7,
                    },
                    "turns": -1,
                    "model": {"id": "x", "provider": "openai", "vendorModelId": "y"},
                },
            )
        ]
    )
    result = mantyx_client.run_agent(system_prompt="x", prompt="y")
    assert result.tokens is not None
    assert result.tokens.input_tokens == 0
    assert result.tokens.cached_tokens == 0
    assert result.tokens.reasoning_tokens == 0
    assert result.tokens.output_tokens == 12
    assert result.turns == 0


def test_run_agent_serialises_tool_refs(
    mantyx_client: MantyxClient, mock_server: MockServer
) -> None:
    mantyx_client.run_agent(
        system_prompt="x",
        prompt="y",
        tools=[
            mantyx_tool("tool_abc"),
            mantyx_plugin_tool("@web/search"),
        ],
    )
    body = mock_server.last_run_create_body
    assert body is not None
    tools = body["tools"]
    assert {"kind": "mantyx", "id": "tool_abc"} in tools
    assert {"kind": "mantyx_plugin", "name": "@web/search"} in tools


def test_run_agent_metadata(mantyx_client: MantyxClient, mock_server: MockServer) -> None:
    mantyx_client.run_agent(
        system_prompt="x",
        prompt="y",
        metadata={"customer": "acme", "env": "prod"},
    )
    body = mock_server.last_run_create_body
    assert body is not None
    assert body["metadata"] == {"customer": "acme", "env": "prod"}


def test_run_agent_id_path(mantyx_client: MantyxClient, mock_server: MockServer) -> None:
    mantyx_client.run_agent(agent_id="agent_xyz", prompt="hi")
    body = mock_server.last_run_create_body
    assert body is not None
    assert body["agentId"] == "agent_xyz"
    assert "systemPrompt" not in body


def test_run_agent_requires_agent_id_or_system_prompt(mantyx_client: MantyxClient) -> None:
    from mantyx import MantyxError

    with pytest.raises(MantyxError):
        mantyx_client.run_agent(prompt="hi")


def test_run_agent_loop_detection_and_tool_budgets(
    mantyx_client: MantyxClient, mock_server: MockServer
) -> None:
    mantyx_client.run_agent(
        system_prompt="x",
        prompt="y",
        loop_detection={"consecutiveThreshold": 4, "hardCutoffThreshold": 8},
        tool_budgets={
            "recall": {"maxCalls": 3},
            "scary_tool": {"maxCalls": 0},
        },
    )
    body = mock_server.last_run_create_body
    assert body is not None
    assert body["loopDetection"] == {
        "consecutiveThreshold": 4,
        "hardCutoffThreshold": 8,
    }
    assert body["toolBudgets"] == {
        "recall": {"maxCalls": 3},
        "scary_tool": {"maxCalls": 0},
    }


def test_run_agent_loop_detection_disabled(
    mantyx_client: MantyxClient, mock_server: MockServer
) -> None:
    mantyx_client.run_agent(system_prompt="x", prompt="y", loop_detection=False)
    body = mock_server.last_run_create_body
    assert body is not None
    assert body["loopDetection"] is False


def test_run_agent_tool_budgets_empty_clears_defaults(
    mantyx_client: MantyxClient, mock_server: MockServer
) -> None:
    mantyx_client.run_agent(system_prompt="x", prompt="y", tool_budgets={})
    body = mock_server.last_run_create_body
    assert body is not None
    assert body["toolBudgets"] == {}


def test_run_agent_loop_detection_invalid_thresholds(
    mantyx_client: MantyxClient,
) -> None:
    with pytest.raises(ValueError):
        mantyx_client.run_agent(
            system_prompt="x",
            prompt="y",
            loop_detection={"consecutiveThreshold": 5, "hardCutoffThreshold": 5},
        )


def test_run_agent_tool_budgets_negative_max_calls(
    mantyx_client: MantyxClient,
) -> None:
    with pytest.raises(ValueError):
        mantyx_client.run_agent(
            system_prompt="x",
            prompt="y",
            tool_budgets={"recall": {"maxCalls": -1}},
        )


def test_stream_agent_yields_events(mantyx_client: MantyxClient, mock_server: MockServer) -> None:
    mock_server.script_for_next_run = RunScript(
        events=[
            ScriptEvent(kind="delta", data={"text": "tick"}),
            ScriptEvent(kind="result", data={"subtype": "success", "text": "tick"}),
        ]
    )
    events = list(mantyx_client.stream_agent(system_prompt="x", prompt="y"))
    types = [ev.type for ev in events]
    assert "assistant_delta" in types
    assert types[-1] == "result"
