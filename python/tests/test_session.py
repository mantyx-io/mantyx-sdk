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


def test_list_sessions_filters_by_metadata(
    mantyx_client: MantyxClient, mock_server: MockServer
) -> None:
    mantyx_client.create_session(system_prompt="x", metadata={"customer": "acme", "env": "prod"})
    mantyx_client.create_session(system_prompt="x", metadata={"customer": "globex", "env": "prod"})

    all_sessions = mantyx_client.list_sessions()
    assert all_sessions.total == 2

    filtered = mantyx_client.list_sessions(metadata={"customer": "acme"})
    assert filtered.total == 1
    assert filtered.sessions[0].metadata == {"customer": "acme", "env": "prod"}
    assert filtered.sessions[0].status == "active"
    assert filtered.sessions[0].session_id

    none = mantyx_client.list_sessions(metadata={"customer": "acme", "env": "staging"})
    assert none.total == 0


def test_get_session_events_replays_frames(
    mantyx_client: MantyxClient, mock_server: MockServer
) -> None:
    session = mantyx_client.create_session(system_prompt="x")
    session.send("one")
    session.send("three")

    full = mantyx_client.get_session_events(session.id)
    assert [(e.seq, e.type, e.text) for e in full] == [
        (1, "user_message", "one"),
        (2, "assistant_message", "echo:one"),
        (3, "user_message", "three"),
        (4, "assistant_message", "echo:three"),
    ]

    last_two = session.events(last_messages=2)
    assert [(e.seq, e.type, e.text) for e in last_two] == [
        (3, "user_message", "three"),
        (4, "assistant_message", "echo:three"),
    ]
