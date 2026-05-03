"""Tests for sessions (create/send/resume/end)."""

from __future__ import annotations

from mantyx import MantyxClient

from .conftest import MockServer


def test_session_send(mantyx_client: MantyxClient, mock_server: MockServer) -> None:
    session = mantyx_client.create_session(system_prompt="You are helpful.")
    r1 = session.send("Hello")
    assert r1.text == "echo:Hello"
    history = session.history()
    assert history == [
        {"role": "user", "content": "Hello"},
        {"role": "assistant", "content": "echo:Hello"},
    ]
    session.end()


def test_session_metadata_on_send(mantyx_client: MantyxClient, mock_server: MockServer) -> None:
    session = mantyx_client.create_session(
        system_prompt="x",
        metadata={"customer": "acme"},
    )
    session.send("hi", metadata={"trace_id": "abc"})
    assert mock_server.last_session_message_body is not None
    assert mock_server.last_session_message_body["metadata"] == {"trace_id": "abc"}
    create_body = mock_server.last_session_create_body
    assert create_body is not None
    assert create_body["metadata"] == {"customer": "acme"}


def test_session_resume(mantyx_client: MantyxClient, mock_server: MockServer) -> None:
    session = mantyx_client.create_session(system_prompt="x")
    sid = session.id
    resumed = mantyx_client.resume_session(sid)
    assert resumed.id == sid
    info = resumed.info()
    assert info.id == sid
    assert info.status == "active"
