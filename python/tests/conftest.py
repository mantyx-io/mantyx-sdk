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
import urllib.parse
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
        # When > 0, the next N API requests return 401; subsequent calls
        # fall through to normal handling. Drives the SDK's refresh +
        # retry-once on 401 path.
        self.fail_auth_count = 0
        # When set, all routes return 403 ``insufficient_scope`` with the
        # configured ``required`` payload (single-element list serialises
        # as a string; longer lists serialise as a JSON array — matches
        # the server contract).
        self.fail_scope: list[str] | None = None
        # When > 0, the next N tool-result POSTs return 503; subsequent
        # calls succeed. Drives the SDK's tool-result retry path.
        self.fail_tool_result_count = 0
        self.tool_result_fixed_status: int | None = None
        self.tool_result_post_count = 0
        self.last_auth_header: str | None = None
        self.auth_header_history: list[str] = []
        # ── OAuth authorization server simulation ───────────────────────
        self.oauth_access_token = "mantyx_at_mock_initial"
        self.oauth_refresh_token = "mantyx_rt_mock_initial"
        self.oauth_expires_in = 3600
        self.oauth_scope = "models:read runs:write"
        self.oauth_rotate_access_token = True
        self.oauth_next_error: dict[str, Any] | None = None
        self.oauth_token_call_count = 0
        self.oauth_last_token_request: dict[str, str] | None = None
        self.oauth_revoke_call_count = 0
        self.oauth_last_revoke_request: dict[str, str] | None = None
        # Optional artificial latency on /token, in seconds.
        self.oauth_token_latency_s = 0.0
        self.last_run_create_body: dict[str, Any] | None = None
        self.last_session_create_body: dict[str, Any] | None = None
        self.last_session_message_body: dict[str, Any] | None = None
        self.last_tool_result_body: dict[str, Any] | None = None
        self.last_feedback_body: dict[str, Any] | None = None
        self.last_feedback_created: bool = False
        self.feedback_by_run: dict[str, dict[str, Any]] = {}
        self.eval_datasets: list[dict[str, Any]] = [
            {
                "id": "ds_demo",
                "name": "Demo dataset",
                "description": None,
                "caseCount": 1,
                "runCount": 0,
                "createdAt": "2026-01-01T00:00:00Z",
                "updatedAt": "2026-01-01T00:00:00Z",
            }
        ]
        self.eval_dataset_details: dict[str, dict[str, Any]] = {
            "ds_demo": {
                "id": "ds_demo",
                "name": "Demo dataset",
                "description": None,
                "toolMocks": None,
                "cases": [
                    {
                        "id": "case_1",
                        "name": "hello",
                        "input": {"role": "user", "content": "hi"},
                        "scorers": [],
                        "tags": [],
                        "toolMocks": None,
                        "createdAt": "2026-01-01T00:00:00Z",
                        "updatedAt": "2026-01-01T00:00:00Z",
                    }
                ],
                "createdAt": "2026-01-01T00:00:00Z",
                "updatedAt": "2026-01-01T00:00:00Z",
            }
        }
        self.eval_runs: dict[str, dict[str, Any]] = {}
        self.last_eval_create_body: dict[str, Any] | None = None
        self.eval_stream_events: list[dict[str, Any]] | None = None
        self.script_for_next_run: RunScript | None = None
        self.session_scripts: dict[str, RunScript] = {}
        # Each session: {"messages": [...], "metadata": {...}, "createdAt": str}
        self.sessions: dict[str, dict[str, Any]] = {}
        self.runs: dict[str, _RunState] = {}
        # Mock A2A peer state — used by define_local_a2a tests so the SDK
        # can fetch a card and POST `message/send` against the same mock.
        self.a2a_agent_card: dict[str, Any] = {
            "name": "Acme HR",
            "description": "Answers HR policy and PTO questions.",
            "url": "http://mock/a2a/rpc",
            "protocolVersion": "0.3.0",
            "skills": [{"id": "pto_lookup", "name": "PTO lookup"}],
        }
        self.a2a_reply_text: str = ""
        self.last_a2a_request: dict[str, Any] | None = None
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
            auth = request.headers.get("authorization")
            self.last_auth_header = auth
            if auth is not None:
                self.auth_header_history.append(auth)
            fail_auth = self.fail_auth
        path = request.url.path
        method = request.method
        # ── OAuth authorization server simulation ────────────────────
        # Not gated by ``fail_auth`` / ``fail_scope`` — these endpoints
        # use their own RFC 6749 error model (invalid_grant / invalid_client).
        if path == "/api/oauth/token" and method == "POST":
            return self._handle_oauth_token(request)
        if path == "/api/oauth/revoke" and method == "POST":
            return self._handle_oauth_revoke(request)
        # ── A2A peer simulation routes ─────────────────────────────────
        if path == "/a2a/agent-card.json" and method == "GET":
            return httpx.Response(200, json=self.a2a_agent_card)
        if path == "/a2a/rpc" and method == "POST":
            body = json.loads(request.content or b"{}")
            params = body.get("params") or {}
            message = params.get("message") or {}
            parts_in = message.get("parts") or []
            text = "\n".join(
                p.get("text", "")
                for p in parts_in
                if isinstance(p, dict) and p.get("kind") == "text"
            )
            with self.lock:
                self.last_a2a_request = {
                    "method": body.get("method"),
                    "message": text,
                    "headers": dict(request.headers),
                }
                reply = self.a2a_reply_text or f"peer reply to: {text}"
            return httpx.Response(
                200,
                json={
                    "jsonrpc": "2.0",
                    "id": body.get("id"),
                    "result": {
                        "kind": "message",
                        "role": "agent",
                        "messageId": "m_test",
                        "parts": [{"kind": "text", "text": reply}],
                    },
                },
            )

        if fail_auth:
            return httpx.Response(401, json={"error": "Invalid API key or OAuth access token"})
        with self.lock:
            consume_fail = self.fail_auth_count > 0
            if consume_fail:
                self.fail_auth_count -= 1
        if consume_fail:
            return httpx.Response(401, json={"error": "Invalid API key or OAuth access token"})
        with self.lock:
            fail_scope = self.fail_scope
        if fail_scope is not None:
            required_payload: Any = fail_scope[0] if len(fail_scope) == 1 else list(fail_scope)
            return httpx.Response(
                403,
                json={"error": "insufficient_scope", "required": required_payload},
                headers={
                    "WWW-Authenticate": (
                        f'Bearer error="insufficient_scope", scope="{" ".join(fail_scope)}"'
                    ),
                },
            )
        parts = [p for p in path.strip("/").split("/") if p]
        if len(parts) < 4 or parts[0] != "api" or parts[1] != "v1" or parts[2] != "workspaces":
            return httpx.Response(404, json={"error": "not_found"})
        rest = parts[4:]
        if rest == ["models"] and method == "GET":
            return httpx.Response(200, json=self.models)
        if rest and rest[0] == "agent-runs":
            return self._handle_agent_runs(request, rest[1:], method)
        if rest and rest[0] == "agent-sessions":
            return self._handle_agent_sessions(request, rest[1:], method)
        if rest and rest[0] == "eval-datasets":
            return self._handle_eval_datasets(request, rest[1:], method)
        if rest and rest[0] == "eval-runs":
            return self._handle_eval_runs(request, rest[1:], method)
        return httpx.Response(404, json={"error": "not_found"})

    # ----- evals -------------------------------------------------------

    def _handle_eval_datasets(
        self, request: httpx.Request, rest: list[str], method: str
    ) -> httpx.Response:
        if not rest and method == "GET":
            return httpx.Response(200, json={"datasets": self.eval_datasets})
        if len(rest) == 1 and method == "GET":
            detail = self.eval_dataset_details.get(rest[0])
            if detail is None:
                return httpx.Response(404, json={"error": "not_found"})
            return httpx.Response(200, json=detail)
        return httpx.Response(404, json={"error": "not_found"})

    def _handle_eval_runs(
        self, request: httpx.Request, rest: list[str], method: str
    ) -> httpx.Response:
        if rest == ["compare"] and method == "GET":
            params = dict(request.url.params)
            run_a = self.eval_runs.get(str(params.get("a") or ""))
            run_b = self.eval_runs.get(str(params.get("b") or ""))
            if run_a is None or run_b is None:
                return httpx.Response(404, json={"error": "not_found"})
            return httpx.Response(
                200,
                json={
                    "datasetId": run_a["datasetId"],
                    "datasetName": run_a["datasetName"],
                    "runA": run_a["summary"],
                    "runB": run_b["summary"],
                    "cases": [],
                },
            )
        if not rest and method == "POST":
            body = json.loads(request.content or b"{}")
            with self.lock:
                self.last_eval_create_body = body
            run_id = _new_id("eval")
            dataset_id = str(body.get("datasetId") or "ds_inline")
            detail = {
                "id": run_id,
                "datasetId": dataset_id,
                "datasetName": "Demo dataset",
                "agentId": body.get("agentId"),
                "inlineAgent": body.get("agent") is not None,
                "status": "succeeded",
                "totalCases": 1,
                "completedCases": 1,
                "passedCases": 1,
                "score": 1.0,
                "tokenUsage": None,
                "error": None,
                "agentOverrides": body.get("overrides"),
                "startedAt": "2026-01-01T00:00:01Z",
                "finishedAt": "2026-01-01T00:00:02Z",
                "createdAt": "2026-01-01T00:00:00Z",
                "inlineAgentSpec": body.get("agent"),
                "agentSpecSnapshot": None,
                "updatedAt": "2026-01-01T00:00:02Z",
                "results": [],
                "summary": {},
            }
            detail["summary"] = {
                k: v
                for k, v in detail.items()
                if k
                not in ("results", "summary", "inlineAgentSpec", "agentSpecSnapshot", "updatedAt")
            }
            self.eval_runs[run_id] = detail
            return httpx.Response(
                202,
                json={
                    "runId": run_id,
                    "status": "queued",
                    "streamUrl": f"/api/v1/workspaces/x/eval-runs/{run_id}/stream",
                },
            )
        if not rest and method == "GET":
            return httpx.Response(
                200,
                json={
                    "runs": [r["summary"] for r in self.eval_runs.values()],
                    "limit": 50,
                    "offset": 0,
                },
            )
        if len(rest) == 1 and method == "GET":
            detail = self.eval_runs.get(rest[0])
            if detail is None:
                return httpx.Response(404, json={"error": "not_found"})
            return httpx.Response(200, json={k: v for k, v in detail.items() if k != "summary"})
        if len(rest) == 2 and rest[1] == "stream" and method == "GET":
            run_id = rest[0]
            if run_id not in self.eval_runs:
                return httpx.Response(404, json={"error": "not_found"})
            events = self.eval_stream_events or [
                {"type": "snapshot", "status": "succeeded"},
                {"type": "run_completed"},
            ]
            lines = [f"data: {json.dumps(ev)}\n\n" for ev in events]
            return httpx.Response(
                200, headers={"content-type": "text/event-stream"}, content="".join(lines)
            )
        if len(rest) == 2 and rest[1] == "cancel" and method == "POST":
            detail = self.eval_runs.get(rest[0])
            if detail is None:
                return httpx.Response(404, json={"error": "not_found"})
            detail["status"] = "cancelled"
            detail["summary"]["status"] = "cancelled"
            return httpx.Response(200, json={"ok": True})
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
                self.tool_result_post_count += 1
                if self.tool_result_fixed_status is not None:
                    return httpx.Response(
                        self.tool_result_fixed_status,
                        json={"error": "fixed_failure"},
                    )
                if self.fail_tool_result_count > 0:
                    self.fail_tool_result_count -= 1
                    return httpx.Response(503, json={"error": "temporary_unavailable"})
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
        if len(rest) == 2 and rest[1] == "feedback" and method == "POST":
            body = json.loads(request.content or b"{}")
            run_id = rest[0]
            with self.lock:
                existing = self.feedback_by_run.get(run_id)
                created = existing is None
                fb_id = existing["id"] if existing else _new_id("fb")
                self.feedback_by_run[run_id] = {"id": fb_id, "body": body}
                self.last_feedback_body = body
                self.last_feedback_created = created
            status = 201 if created else 200
            return httpx.Response(
                status,
                json={
                    "id": fb_id,
                    "verdict": body.get("verdict"),
                    "targetKind": "agent_run",
                    "agentRunId": run_id,
                },
            )
        return httpx.Response(404, json={"error": "not_found"})

    # ----- agent-sessions ----------------------------------------------

    def _handle_agent_sessions(
        self, request: httpx.Request, rest: list[str], method: str
    ) -> httpx.Response:
        if not rest and method == "POST":
            body = json.loads(request.content or b"{}")
            sid = _new_id("sess")
            metadata = body.get("metadata") if isinstance(body.get("metadata"), dict) else {}
            with self.lock:
                self.last_session_create_body = body
                self.sessions[sid] = {
                    "messages": [],
                    "metadata": metadata,
                    "createdAt": "2026-01-01T00:00:00.000Z",
                }
            return httpx.Response(
                201,
                json={
                    "sessionId": sid,
                    "name": "ephemeral",
                    "metadata": metadata,
                    "createdAt": "2026-01-01T00:00:00.000Z",
                },
            )
        # GET /agent-sessions — list, with optional repeated ?metadata=key:value
        if not rest and method == "GET":
            q = request.url.params
            raw_filters = q.get_list("metadata")
            filters: list[tuple[str, str]] = []
            for raw in raw_filters:
                idx = raw.find(":")
                if idx <= 0:
                    return httpx.Response(400, json={"error": "Invalid metadata filter"})
                filters.append((raw[:idx], raw[idx + 1 :]))
            with self.lock:
                items = list(self.sessions.items())
            matched = [
                (sid, s) for sid, s in items if all(s["metadata"].get(k) == v for k, v in filters)
            ]
            sessions = [
                {
                    "sessionId": sid,
                    "creationDate": s["createdAt"],
                    "lastInteractionDate": s["createdAt"],
                    "summary": next(
                        (m["content"] for m in s["messages"] if m["role"] == "user"), ""
                    ),
                    "metadata": s["metadata"],
                    "status": "active",
                }
                for sid, s in matched
            ]
            return httpx.Response(
                200,
                json={"total": len(sessions), "limit": 50, "offset": 0, "sessions": sessions},
            )
        # GET /agent-sessions/:id/events — replay as realtime-style frames
        if len(rest) == 2 and rest[1] == "events" and method == "GET":
            with self.lock:
                session = self.sessions.get(rest[0])
            if session is None:
                return httpx.Response(404, json={"error": "not_found"})
            all_messages = session["messages"]
            q = request.url.params
            full = q.get("full") in ("1", "true")
            selected = all_messages
            if not full and q.get("lastMessages"):
                try:
                    n = int(q.get("lastMessages") or "0")
                    if n > 0:
                        selected = all_messages[-n:]
                except ValueError:
                    pass
            start_index = len(all_messages) - len(selected)
            events = []
            for i, m in enumerate(selected):
                seq = start_index + i + 1
                if m["role"] == "assistant":
                    events.append({"seq": seq, "type": "assistant_message", "text": m["content"]})
                elif m["role"] == "user":
                    events.append({"seq": seq, "type": "user_message", "text": m["content"]})
                else:
                    events.append(
                        {"seq": seq, "type": "message", "role": m["role"], "text": m["content"]}
                    )
            return httpx.Response(
                200, json={"sessionId": rest[0], "total": len(all_messages), "events": events}
            )
        if len(rest) == 1 and method == "GET":
            with self.lock:
                session = self.sessions.get(rest[0])
            if session is None:
                return httpx.Response(404, json={"error": "not_found"})
            return httpx.Response(
                200,
                json={
                    "id": rest[0],
                    "name": "ephemeral",
                    "status": "active",
                    "createdAt": session["createdAt"],
                    "lastUsedAt": "",
                    "endedAt": None,
                    "agentSpec": {},
                    "messages": session["messages"],
                    "metadata": session["metadata"],
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
                self.sessions[session_id]["messages"].extend(
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

    # ----- OAuth -------------------------------------------------------

    def _handle_oauth_token(self, request: httpx.Request) -> httpx.Response:
        import time

        with self.lock:
            self.oauth_token_call_count += 1
        form = _parse_form(request.content or b"")
        with self.lock:
            self.oauth_last_token_request = form
            latency = self.oauth_token_latency_s
            err = self.oauth_next_error
            self.oauth_next_error = None
            access = self.oauth_access_token
            refresh = self.oauth_refresh_token
            scope = self.oauth_scope
            expires_in = self.oauth_expires_in
            rotate = self.oauth_rotate_access_token
            call_idx = self.oauth_token_call_count
        if latency > 0:
            time.sleep(latency)
        if err:
            payload: dict[str, Any] = {"error": err.get("error", "invalid_request")}
            if "description" in err:
                payload["error_description"] = err["description"]
            return httpx.Response(int(err.get("status", 400)), json=payload)
        grant = form.get("grant_type")
        if grant not in ("authorization_code", "refresh_token", "client_credentials"):
            return httpx.Response(400, json={"error": "unsupported_grant_type"})
        access_token = f"{access}_v{call_idx}" if rotate else access
        response: dict[str, Any] = {
            "access_token": access_token,
            "token_type": "Bearer",
            "expires_in": expires_in,
            "scope": form.get("scope") or scope,
        }
        # ``refresh_token`` grants echo the same value the client sent
        # (non-rotating per docs/oauth.md); ``client_credentials`` never
        # issues one; ``authorization_code`` returns the persistent value.
        if grant == "refresh_token":
            response["refresh_token"] = form.get("refresh_token") or refresh
        elif grant == "authorization_code":
            response["refresh_token"] = refresh
        return httpx.Response(200, json=response)

    def _handle_oauth_revoke(self, request: httpx.Request) -> httpx.Response:
        form = _parse_form(request.content or b"")
        with self.lock:
            self.oauth_revoke_call_count += 1
            self.oauth_last_revoke_request = form
        return httpx.Response(200, json={})

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
            if ev.kind in ("result", "error", "cancelled"):
                return


def _parse_form(raw: bytes) -> dict[str, str]:
    return {k: v for k, v in urllib.parse.parse_qsl(raw.decode("utf-8"), keep_blank_values=True)}


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
