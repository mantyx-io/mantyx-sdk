"""Test fixtures: an in-memory mock of the MANTYX agent-runs HTTP surface.

Built on ``httpx.MockTransport`` so the SDK is exercised end-to-end (URL
construction, JSON encoding, SSE parsing, tool-result POSTs) without
touching the network.

Mirrors the Go SDK's ``mock_server_test.go``: each test gets a fresh
``MockServer`` that scripts the next run with a list of ``ScriptEvent``s
(``delta`` / ``local_tool_call`` / ``result``).
"""

from __future__ import annotations

import json
import threading
from collections.abc import Iterator
from dataclasses import dataclass, field
from typing import Any

import httpx
import pytest


@dataclass
class ScriptEvent:
    kind: str  # "delta" | "local_tool_call" | "result" | "error"
    data: dict[str, Any] = field(default_factory=dict)
    wait_for_result: bool = False  # for local_tool_call: pause until tool-result POST


@dataclass
class RunScript:
    events: list[ScriptEvent] = field(default_factory=list)
    final_text: str = "ok"


_id_counter = 0


def _new_id(prefix: str) -> str:
    global _id_counter
    _id_counter += 1
    return f"{prefix}_{_id_counter}"


def _event_type(kind: str) -> str:
    return {
        "delta": "assistant_delta",
        "result": "result",
        "local_tool_call": "local_tool_call",
        "error": "error",
    }.get(kind, kind)


class MockServer:
    """In-memory MANTYX server-state for tests."""

    def __init__(self) -> None:
        self.lock = threading.Lock()
        self.fail_auth = False
        self.last_auth_header: str | None = None
        self.last_run_create_body: dict[str, Any] | None = None
        self.last_session_create_body: dict[str, Any] | None = None
        self.last_session_message_body: dict[str, Any] | None = None
        self.last_tool_result_body: dict[str, Any] | None = None
        self.script_for_next_run: RunScript | None = None
        self.session_scripts: dict[str, RunScript] = {}
        self.sessions: dict[str, list[dict[str, str]]] = {}
        self.runs: dict[str, _RunState] = {}
        self.models = {
            "models": [
                {
                    "id": "platform:demo",
                    "label": "Demo Platform",
                    "provider": "openai",
                    "vendorModelId": "gpt-test",
                    "source": "platform_offering",
                    "contextWindowTokens": 8000,
                    "pricing": None,
                }
            ],
            "defaultModelId": "platform:demo",
        }

    # ----- HTTP handler ------------------------------------------------

    def handle(self, request: httpx.Request) -> httpx.Response:
        with self.lock:
            self.last_auth_header = request.headers.get("authorization")
            fail_auth = self.fail_auth
        if fail_auth:
            return httpx.Response(401, json={"error": "Invalid API key"})
        path = request.url.path
        parts = [p for p in path.strip("/").split("/") if p]
        if len(parts) < 4 or parts[0] != "api" or parts[1] != "v1" or parts[2] != "workspaces":
            return httpx.Response(404, json={"error": "not_found"})
        rest = parts[4:]
        method = request.method
        if rest == ["models"] and method == "GET":
            return httpx.Response(200, json=self.models)
        if rest and rest[0] == "agent-runs":
            return self._handle_agent_runs(request, rest[1:], method)
        if rest and rest[0] == "agent-sessions":
            return self._handle_agent_sessions(request, rest[1:], method)
        return httpx.Response(404, json={"error": "not_found"})

    # ----- agent-runs --------------------------------------------------

    def _handle_agent_runs(
        self, request: httpx.Request, rest: list[str], method: str
    ) -> httpx.Response:
        if not rest and method == "POST":
            body = json.loads(request.content or b"{}")
            with self.lock:
                self.last_run_create_body = body
                script = self.script_for_next_run or RunScript(
                    events=[ScriptEvent(kind="result", data={"subtype": "success", "text": "ok"})]
                )
                self.script_for_next_run = None
            run_id = _new_id("run")
            self._start_run(run_id, script)
            return httpx.Response(
                202,
                json={
                    "runId": run_id,
                    "streamUrl": f"/api/v1/workspaces/x/agent-runs/{run_id}/stream",
                },
            )
        if len(rest) == 2 and rest[1] == "stream" and method == "GET":
            return self._handle_sse(request, rest[0])
        if len(rest) == 2 and rest[1] == "tool-results" and method == "POST":
            body = json.loads(request.content or b"{}")
            with self.lock:
                self.last_tool_result_body = body
                state = self.runs.get(rest[0])
                if state is not None:
                    state.resolve(body.get("toolUseId") or "")
            return httpx.Response(200, json={"ok": True})
        if len(rest) == 2 and rest[1] == "cancel" and method == "POST":
            with self.lock:
                state = self.runs.get(rest[0])
                if state is not None:
                    state.cancel()
            return httpx.Response(200, json={"ok": True, "status": "cancelled"})
        return httpx.Response(404, json={"error": "not_found"})

    # ----- agent-sessions ----------------------------------------------

    def _handle_agent_sessions(
        self, request: httpx.Request, rest: list[str], method: str
    ) -> httpx.Response:
        if not rest and method == "POST":
            body = json.loads(request.content or b"{}")
            sid = _new_id("sess")
            with self.lock:
                self.last_session_create_body = body
                self.sessions[sid] = []
            return httpx.Response(
                201, json={"sessionId": sid, "name": "ephemeral", "createdAt": "now"}
            )
        if len(rest) == 1 and method == "GET":
            with self.lock:
                msgs = self.sessions.get(rest[0])
            if msgs is None:
                return httpx.Response(404, json={"error": "not_found"})
            return httpx.Response(
                200,
                json={
                    "id": rest[0],
                    "name": "ephemeral",
                    "status": "active",
                    "createdAt": "",
                    "lastUsedAt": "",
                    "endedAt": None,
                    "agentSpec": {},
                    "messages": msgs,
                    "metadata": {},
                },
            )
        if len(rest) == 1 and method == "DELETE":
            with self.lock:
                self.sessions.pop(rest[0], None)
            return httpx.Response(200, json={"ok": True})
        if len(rest) == 2 and rest[1] == "messages" and method == "POST":
            body = json.loads(request.content or b"{}")
            with self.lock:
                self.last_session_message_body = body
                session_id = rest[0]
                if session_id not in self.sessions:
                    return httpx.Response(404, json={"error": "not_found"})
                script = self.session_scripts.pop(
                    session_id,
                    RunScript(
                        events=[
                            ScriptEvent(
                                kind="result",
                                data={
                                    "subtype": "success",
                                    "text": f"echo:{body.get('prompt', '')}",
                                },
                            )
                        ]
                    ),
                )
                final_text = _last_result_text(script)
                self.sessions[session_id].extend(
                    [
                        {"role": "user", "content": str(body.get("prompt", ""))},
                        {"role": "assistant", "content": final_text},
                    ]
                )
            run_id = _new_id("run")
            self._start_run(run_id, script)
            return httpx.Response(
                202,
                json={
                    "runId": run_id,
                    "streamUrl": f"/api/v1/workspaces/x/agent-runs/{run_id}/stream",
                },
            )
        return httpx.Response(404, json={"error": "not_found"})

    # ----- run lifecycle ----------------------------------------------

    def _start_run(self, run_id: str, script: RunScript) -> None:
        state = _RunState(script)
        with self.lock:
            self.runs[run_id] = state

    def _handle_sse(self, request: httpx.Request, run_id: str) -> httpx.Response:
        with self.lock:
            state = self.runs.get(run_id)
        if state is None:
            return httpx.Response(404, json={"error": "not_found"})
        from_seq = 0
        last_evt_id = request.headers.get("last-event-id")
        if last_evt_id:
            try:
                from_seq = int(last_evt_id)
            except ValueError:
                pass
        q = request.url.params
        if q.get("lastSeq"):
            try:
                from_seq = int(q.get("lastSeq") or "0")
            except ValueError:
                pass

        # Buffer the whole stream into a single bytes blob — httpx adapts it
        # to either sync `iter_bytes()` or async `aiter_bytes()` automatically,
        # which lets the same MockServer back both client variants. The SDK's
        # tool-result POST still arrives during the same run because the
        # ThreadPoolExecutor / asyncio.gather in the SDK awaits the dispatch
        # before the iterator is closed.
        buf = b"".join(state.iter_sse(from_seq))
        return httpx.Response(
            200,
            headers={
                "content-type": "text/event-stream; charset=utf-8",
                "cache-control": "no-cache, no-transform",
            },
            content=buf,
        )


@dataclass
class _RunState:
    script: RunScript
    cursor: int = 0
    cancelled: bool = False
    resolved: dict[str, threading.Event] = field(default_factory=dict)
    lock: threading.Lock = field(default_factory=threading.Lock)

    def cancel(self) -> None:
        with self.lock:
            self.cancelled = True
            for e in self.resolved.values():
                e.set()

    def resolve(self, tool_use_id: str) -> None:
        with self.lock:
            ev = self.resolved.get(tool_use_id)
        if ev is not None:
            ev.set()

    def iter_sse(self, from_seq: int) -> Iterator[bytes]:
        # Emits the whole script eagerly. ``wait_for_result`` is informational
        # only; the SDK's tool dispatch still completes before the iterator
        # closes (ThreadPoolExecutor / asyncio.gather wait for it), so tests
        # see ``last_tool_result_body`` populated after the run returns.
        seq = 0
        for ev in self.script.events:
            seq += 1
            if seq <= from_seq:
                continue
            data = {"seq": seq, **ev.data}
            evt_type = _event_type(ev.kind)
            yield (f"id: {seq}\nevent: {evt_type}\ndata: {json.dumps(data)}\n\n".encode())
            if ev.kind == "result":
                return


def _last_result_text(script: RunScript) -> str:
    for ev in reversed(script.events):
        if ev.kind == "result":
            t = ev.data.get("text")
            if isinstance(t, str):
                return t
    return script.final_text


# ----- pytest fixtures -------------------------------------------------


@pytest.fixture
def mock_server() -> MockServer:
    return MockServer()


@pytest.fixture
def http_client(mock_server: MockServer) -> Iterator[httpx.Client]:
    transport = httpx.MockTransport(mock_server.handle)
    with httpx.Client(transport=transport, base_url="http://mock") as c:
        yield c


@pytest.fixture
def mantyx_client(mock_server: MockServer) -> Iterator[MantyxClient]:  # type: ignore[name-defined]  # noqa: F821
    from mantyx import MantyxClient

    transport = httpx.MockTransport(mock_server.handle)
    http = httpx.Client(transport=transport, base_url="http://mock", headers={"X": "y"})
    client = MantyxClient(
        api_key="test-key",
        workspace_slug="acme",
        base_url="http://mock",
        http_client=http,
    )
    yield client
    client.close()


@pytest.fixture
async def async_mantyx_client(mock_server: MockServer):
    from mantyx import AsyncMantyxClient

    transport = httpx.MockTransport(mock_server.handle)
    http = httpx.AsyncClient(transport=transport, base_url="http://mock")
    client = AsyncMantyxClient(
        api_key="test-key",
        workspace_slug="acme",
        base_url="http://mock",
        http_client=http,
    )
    try:
        yield client
    finally:
        await client.aclose()
