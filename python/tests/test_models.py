"""Tests for ``MantyxClient.list_models``."""

from __future__ import annotations

from mantyx import MantyxClient


def test_list_models(mantyx_client: MantyxClient) -> None:
    catalog = mantyx_client.list_models()
    assert catalog.default_model_id == "platform:demo"
    assert len(catalog.models) == 1
    m = catalog.models[0]
    assert m.id == "platform:demo"
    assert m.label == "Demo Platform"
    assert m.provider == "openai"
    assert m.vendor_model_id == "gpt-test"
    assert m.source == "platform_offering"
    assert m.context_window_tokens == 8000


def test_auth_header(mantyx_client: MantyxClient, mock_server) -> None:
    mantyx_client.list_models()
    assert mock_server.last_auth_header == "Bearer test-key"
