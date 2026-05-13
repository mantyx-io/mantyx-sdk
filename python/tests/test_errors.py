"""Tests for the error hierarchy."""

from __future__ import annotations

import httpx
import pytest

from mantyx import (
    MantyxAuthError,
    MantyxClient,
    MantyxError,
    MantyxRunError,
    MantyxScopeError,
    define_local_tool,
)

from .conftest import MockServer


def test_auth_error_on_401(mantyx_client: MantyxClient, mock_server: MockServer) -> None:
    mock_server.fail_auth = True
    with pytest.raises(MantyxAuthError):
        mantyx_client.list_models()


def test_auth_error_default_message_mentions_both_credentials() -> None:
    err = MantyxAuthError()
    assert "API key" in err.message
    assert "OAuth" in err.message


def test_constructor_validates_inputs() -> None:
    with pytest.raises(MantyxError):
        MantyxClient(api_key="", workspace_slug="acme")
    with pytest.raises(MantyxError):
        MantyxClient(api_key="x", workspace_slug="")


def test_constructor_accepts_access_token_alias(mock_server: MockServer) -> None:
    transport = httpx.MockTransport(mock_server.handle)
    http = httpx.Client(transport=transport, base_url="http://mock")
    client = MantyxClient(
        access_token="mantyx_at_test",
        workspace_slug="acme",
        base_url="http://mock",
        http_client=http,
    )
    try:
        assert client.api_key == "mantyx_at_test"
        client.list_models()
        assert mock_server.last_auth_header == "Bearer mantyx_at_test"
    finally:
        client.close()


def test_constructor_rejects_both_credentials() -> None:
    with pytest.raises(MantyxError):
        MantyxClient(
            api_key="mantyx_x",
            access_token="mantyx_at_y",
            workspace_slug="acme",
        )


def test_constructor_requires_at_least_one_credential() -> None:
    with pytest.raises(MantyxError):
        MantyxClient(workspace_slug="acme")


def test_scope_error_carries_required_scopes(
    mantyx_client: MantyxClient, mock_server: MockServer
) -> None:
    mock_server.fail_scope = ["runs:write"]
    with pytest.raises(MantyxScopeError) as exc_info:
        mantyx_client.list_models()
    err = exc_info.value
    assert err.code == "insufficient_scope"
    assert err.status == 403
    assert err.required_scopes == ("runs:write",)


def test_scope_error_handles_multi_scope_routes(
    mantyx_client: MantyxClient, mock_server: MockServer
) -> None:
    mock_server.fail_scope = ["runs:read", "runs:write"]
    with pytest.raises(MantyxScopeError) as exc_info:
        mantyx_client.list_models()
    assert exc_info.value.required_scopes == ("runs:read", "runs:write")


def test_local_tool_name_validation() -> None:
    with pytest.raises(ValueError):
        define_local_tool(name="bad name!", execute=lambda _: "")
    with pytest.raises(ValueError):
        define_local_tool(name="", execute=lambda _: "")
    # Valid names are accepted.
    t = define_local_tool(name="ok_tool_42", execute=lambda _: "")
    assert t.name == "ok_tool_42"


def test_plugin_tool_name_validation() -> None:
    from mantyx import mantyx_plugin_tool

    with pytest.raises(ValueError):
        mantyx_plugin_tool("not-a-plugin")
    with pytest.raises(ValueError):
        mantyx_plugin_tool("@web")
    ref = mantyx_plugin_tool("@web/search")
    assert ref.name == "@web/search"


def test_run_error_carries_optional_triage_attributes() -> None:
    err = MantyxRunError(
        "run_1",
        "truncation",
        "Model output was truncated.",
        error_class="truncation",
        finish_reason="max_tokens",
        partial_text='{"answer":"hi',
        retryable=False,
    )
    assert err.run_id == "run_1"
    assert err.subtype == "truncation"
    assert err.code == "truncation"
    assert err.error_class == "truncation"
    assert err.finish_reason == "max_tokens"
    assert err.partial_text == '{"answer":"hi'
    assert err.retryable is False


def test_run_error_defaults_triage_attributes_to_none() -> None:
    err = MantyxRunError("run_2", "error", "boom")
    assert err.error_class is None
    assert err.finish_reason is None
    assert err.partial_text is None
    assert err.retryable is None
