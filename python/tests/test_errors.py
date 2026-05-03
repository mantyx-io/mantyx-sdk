"""Tests for the error hierarchy."""

from __future__ import annotations

import pytest

from mantyx import (
    MantyxAuthError,
    MantyxClient,
    MantyxError,
    define_local_tool,
)

from .conftest import MockServer


def test_auth_error_on_401(mantyx_client: MantyxClient, mock_server: MockServer) -> None:
    mock_server.fail_auth = True
    with pytest.raises(MantyxAuthError):
        mantyx_client.list_models()


def test_constructor_validates_inputs() -> None:
    with pytest.raises(MantyxError):
        MantyxClient(api_key="", workspace_slug="acme")
    with pytest.raises(MantyxError):
        MantyxClient(api_key="x", workspace_slug="")


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
