"""Synchronous MANTYX client: HTTP plumbing, model catalog, run + session drivers.

The wire protocol is identical to the TypeScript and Go SDKs and is
specified in ``docs/agent-runs-protocol.md`` (a copy ships with this
package under ``docs/``).
"""

from __future__ import annotations

import json
import time
from collections.abc import Callable, Iterable, Iterator, Mapping, Sequence
from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass, field
from typing import (
    Any,
    cast,
)

import httpx

from ._local_resolver import (
    _SyncMcpPortal,
    sync_call_a2a,
    sync_close_mcp_refs,
    sync_resolve_local_refs,
)
from ._schema import parse_args_with_pydantic
from ._version import SDK_VERSION
from .errors import (
    MantyxAuthError,
    MantyxError,
    MantyxNetworkError,
    MantyxParseError,
    MantyxRunError,
    MantyxToolError,
)
from .sse import SseEvent, iter_sse
from .tools import (
    LoopDetection,
    OutputSchema,
    ReasoningLevel,
    ToolBudgets,
    ToolRef,
    _LocalHandlers,
    collect_local_handlers,
    normalize_loop_detection,
    normalize_output_schema,
    normalize_reasoning_level,
    normalize_tool_budgets,
    serialize_tool_refs,
)

DEFAULT_BASE_URL = "https://app.mantyx.io"
DEFAULT_TIMEOUT_S = 60.0

# Sentinel value for "argument not provided". Distinct from ``None`` because
# ``None``/``False`` are valid wire values for ``loop_detection`` (``False``
# disables the guard) — the helpers need to tell "omit" from "set to that".
_UNSET: Any = object()


# --------------------------------------------------------------------- Data


@dataclass
class PricingInfo:
    inputPer1MUsd: float | None = None
    outputPer1MUsd: float | None = None
    cacheReadPer1MUsd: float | None = None


@dataclass
class ModelInfo:
    id: str
    label: str
    provider: str
    vendor_model_id: str
    source: str  # "workspace_provider" | "platform_offering"
    context_window_tokens: int | None
    pricing: PricingInfo | None


@dataclass
class ModelCatalog:
    models: list[ModelInfo]
    default_model_id: str | None


@dataclass
class RunEvent:
    """One durable run event. Specific payload fields vary by ``type``."""

    seq: int
    type: str
    data: dict[str, Any] = field(default_factory=dict)

    @property
    def text(self) -> str:
        """Convenience for `assistant_delta` / `assistant_message` events."""
        v = self.data.get("text")
        return v if isinstance(v, str) else ""


@dataclass
class RunResult:
    run_id: str
    text: str
    events: list[RunEvent]


@dataclass
class SessionInfo:
    id: str
    name: str
    status: str
    created_at: str
    last_used_at: str
    ended_at: str | None
    agent_spec: dict[str, Any]
    messages: list[dict[str, str]]
    metadata: dict[str, str]


# --------------------------------------------------------------------- Client


class MantyxClient:
    """Synchronous MANTYX client.

    Example:
        >>> client = MantyxClient(api_key="...", workspace_slug="acme-corp")
        >>> result = client.run_agent(
        ...     system_prompt="You are a helpful assistant.",
        ...     prompt="What's the capital of France?",
        ... )
        >>> print(result.text)
    """

    def __init__(
        self,
        *,
        api_key: str,
        workspace_slug: str,
        base_url: str = DEFAULT_BASE_URL,
        timeout: float = DEFAULT_TIMEOUT_S,
        http_client: httpx.Client | None = None,
    ) -> None:
        if not api_key or not isinstance(api_key, str):
            raise MantyxError("api_key is required")
        if not workspace_slug or not isinstance(workspace_slug, str):
            raise MantyxError("workspace_slug is required")
        self.api_key = api_key
        self.workspace_slug = workspace_slug
        self.base_url = base_url.rstrip("/")
        self.timeout = timeout
        self._owns_http = http_client is None
        self._http = http_client or httpx.Client(
            timeout=httpx.Timeout(timeout, connect=10.0, read=None),
            headers={"User-Agent": f"mantyx-sdk-python/{SDK_VERSION}"},
        )
        # Lazily started on the first `mcp_local` use so apps that never use
        # MCP don't pay the daemon-thread cost. Closed by `close()`.
        self._mcp_portal = _SyncMcpPortal()

    # ------------------------------------------------------------------ ctx

    def __enter__(self) -> MantyxClient:
        return self

    def __exit__(self, *exc: Any) -> None:
        self.close()

    def close(self) -> None:
        if self._owns_http:
            self._http.close()
        self._mcp_portal.close()

    # --------------------------------------------------------------- Models

    def list_models(self) -> ModelCatalog:
        body = self._request("GET", "/models")
        return _parse_model_catalog(body or {})

    # -------------------------------------------------------------- Runs

    def run_agent(
        self,
        *,
        prompt: str | None = None,
        messages: Sequence[Mapping[str, str]] | None = None,
        system_prompt: str | None = None,
        agent_id: str | None = None,
        model_id: str | None = None,
        name: str | None = None,
        tools: Sequence[ToolRef] | None = None,
        reasoning_level: ReasoningLevel | None = None,
        output_schema: OutputSchema | Mapping[str, Any] | None = None,
        loop_detection: LoopDetection | Mapping[str, Any] | bool | None = _UNSET,
        tool_budgets: ToolBudgets | Mapping[str, Mapping[str, Any]] | None = _UNSET,
        budgets: Mapping[str, Any] | None = None,
        metadata: Mapping[str, str] | None = None,
        on_assistant_delta: Callable[[str], None] | None = None,
        on_event: Callable[[RunEvent], None] | None = None,
    ) -> RunResult:
        tools_list: list[ToolRef] | None = list(tools) if tools else None
        # Resolve every `a2a_local` agent card and open every `mcp_local`
        # transport before submitting; the resolver mutates the refs in
        # place so the subsequent `_serialize_agent_spec` reads the
        # resolved data.
        sync_resolve_local_refs(tools_list, http=self._http, portal=self._mcp_portal)
        try:
            body = _serialize_agent_spec(
                agent_id=agent_id,
                system_prompt=system_prompt,
                model_id=model_id,
                name=name,
                tools=tools_list,
                reasoning_level=reasoning_level,
                output_schema=output_schema,
                loop_detection=loop_detection,
                tool_budgets=tool_budgets,
                budgets=budgets,
                metadata=metadata,
            )
            if prompt is not None:
                body["prompt"] = prompt
            if messages is not None:
                body["messages"] = list(messages)

            created = self._request("POST", "/agent-runs", body)
            run_id = str((created or {}).get("runId") or "")
            if not run_id:
                raise MantyxError("server did not return a runId")
            handlers = collect_local_handlers(tools_list)
            return self._drive_run(
                run_id=run_id,
                handlers=handlers,
                on_assistant_delta=on_assistant_delta,
                on_event=on_event,
            )
        finally:
            # One-shot runs own their MCP transports; close on exit.
            sync_close_mcp_refs(tools_list)

    def stream_agent(
        self,
        *,
        prompt: str | None = None,
        messages: Sequence[Mapping[str, str]] | None = None,
        system_prompt: str | None = None,
        agent_id: str | None = None,
        model_id: str | None = None,
        name: str | None = None,
        tools: Sequence[ToolRef] | None = None,
        reasoning_level: ReasoningLevel | None = None,
        output_schema: OutputSchema | Mapping[str, Any] | None = None,
        loop_detection: LoopDetection | Mapping[str, Any] | bool | None = _UNSET,
        tool_budgets: ToolBudgets | Mapping[str, Mapping[str, Any]] | None = _UNSET,
        budgets: Mapping[str, Any] | None = None,
        metadata: Mapping[str, str] | None = None,
    ) -> Iterator[RunEvent]:
        tools_list: list[ToolRef] | None = list(tools) if tools else None
        sync_resolve_local_refs(tools_list, http=self._http, portal=self._mcp_portal)
        body = _serialize_agent_spec(
            agent_id=agent_id,
            system_prompt=system_prompt,
            model_id=model_id,
            name=name,
            tools=tools_list,
            reasoning_level=reasoning_level,
            output_schema=output_schema,
            loop_detection=loop_detection,
            tool_budgets=tool_budgets,
            budgets=budgets,
            metadata=metadata,
        )
        if prompt is not None:
            body["prompt"] = prompt
        if messages is not None:
            body["messages"] = list(messages)

        created = self._request("POST", "/agent-runs", body)
        run_id = str((created or {}).get("runId") or "")
        if not run_id:
            sync_close_mcp_refs(tools_list)
            raise MantyxError("server did not return a runId")
        handlers = collect_local_handlers(tools_list)

        def _gen() -> Iterator[RunEvent]:
            try:
                yield from self._stream_events(run_id, handlers)
            finally:
                sync_close_mcp_refs(tools_list)

        return _gen()

    def cancel_run(self, run_id: str) -> None:
        self._request("POST", f"/agent-runs/{_quote(run_id)}/cancel")

    # ----------------------------------------------------------- Sessions

    def create_session(
        self,
        *,
        system_prompt: str | None = None,
        agent_id: str | None = None,
        model_id: str | None = None,
        name: str | None = None,
        tools: Sequence[ToolRef] | None = None,
        reasoning_level: ReasoningLevel | None = None,
        output_schema: OutputSchema | Mapping[str, Any] | None = None,
        loop_detection: LoopDetection | Mapping[str, Any] | bool | None = _UNSET,
        tool_budgets: ToolBudgets | Mapping[str, Mapping[str, Any]] | None = _UNSET,
        budgets: Mapping[str, Any] | None = None,
        metadata: Mapping[str, str] | None = None,
    ) -> AgentSession:
        tools_list: list[ToolRef] | None = list(tools) if tools else None
        # Resolve once at session creation; the session keeps the resolved
        # cards / live MCP connections for its lifetime.
        sync_resolve_local_refs(tools_list, http=self._http, portal=self._mcp_portal)
        try:
            body = _serialize_agent_spec(
                agent_id=agent_id,
                system_prompt=system_prompt,
                model_id=model_id,
                name=name,
                tools=tools_list,
                reasoning_level=reasoning_level,
                output_schema=output_schema,
                loop_detection=loop_detection,
                tool_budgets=tool_budgets,
                budgets=budgets,
                metadata=metadata,
            )
            created = self._request("POST", "/agent-sessions", body) or {}
        except Exception:
            sync_close_mcp_refs(tools_list)
            raise
        session_id = str(created.get("sessionId") or "")
        if not session_id:
            sync_close_mcp_refs(tools_list)
            raise MantyxError("server did not return a sessionId")
        handlers = collect_local_handlers(tools_list)
        return AgentSession(self, id=session_id, handlers=handlers, tools_for_resume=tools_list)

    def resume_session(
        self,
        session_id: str,
        *,
        tools: Sequence[ToolRef] | None = None,
    ) -> AgentSession:
        # Verify the session exists.
        self.get_session_info(session_id)
        tools_list: list[ToolRef] | None = list(tools) if tools else None
        if tools_list:
            sync_resolve_local_refs(tools_list, http=self._http, portal=self._mcp_portal)
        handlers = collect_local_handlers(tools_list)
        return AgentSession(self, id=session_id, handlers=handlers, tools_for_resume=tools_list)

    def end_session(self, session_id: str) -> None:
        self._request("DELETE", f"/agent-sessions/{_quote(session_id)}")

    def get_session_info(self, session_id: str) -> SessionInfo:
        body = self._request("GET", f"/agent-sessions/{_quote(session_id)}") or {}
        return _parse_session_info(body)

    # ------------------------------------------------------------ Internals

    def _drive_run(
        self,
        *,
        run_id: str,
        handlers: _LocalHandlers,
        on_assistant_delta: Callable[[str], None] | None = None,
        on_event: Callable[[RunEvent], None] | None = None,
    ) -> RunResult:
        collected: list[RunEvent] = []
        final_text = ""
        for ev in self._stream_events(run_id, handlers):
            collected.append(ev)
            if on_event is not None:
                on_event(ev)
            if ev.type == "assistant_delta" and on_assistant_delta is not None:
                t = ev.data.get("text")
                if isinstance(t, str):
                    on_assistant_delta(t)
            if ev.type == "result":
                subtype = str(ev.data.get("subtype") or "")
                if subtype == "success":
                    txt = ev.data.get("text")
                    final_text = txt if isinstance(txt, str) else ""
                else:
                    msg = ev.data.get("error") or subtype or "run failed"
                    raise MantyxRunError(run_id, subtype or "error", str(msg))
            elif ev.type == "error":
                # The wire reports both a coarse `code` (legacy alias)
                # and a canonical `errorClass` triage category; prefer
                # `errorClass` for the run-error subtype when present so
                # callers see a stable taxonomy. See
                # `docs/agent-runs-protocol.md` §7.
                error_class_raw = ev.data.get("errorClass")
                error_class = str(error_class_raw) if isinstance(error_class_raw, str) else None
                subtype = error_class or str(ev.data.get("code") or "error")
                finish_raw = ev.data.get("finishReason")
                finish_reason = str(finish_raw) if isinstance(finish_raw, str) else None
                partial_raw = ev.data.get("partialText")
                partial_text = str(partial_raw) if isinstance(partial_raw, str) else None
                retryable_raw = ev.data.get("retryable")
                retryable = retryable_raw if isinstance(retryable_raw, bool) else None
                raise MantyxRunError(
                    run_id,
                    subtype,
                    str(ev.data.get("error") or "error"),
                    error_class=error_class,
                    finish_reason=finish_reason,
                    partial_text=partial_text,
                    retryable=retryable,
                )
            elif ev.type == "cancelled":
                raise MantyxRunError(run_id, "cancelled", "Run was cancelled")
        return RunResult(run_id=run_id, text=final_text, events=collected)

    def _stream_events(
        self,
        run_id: str,
        handlers: _LocalHandlers,
    ) -> Iterator[RunEvent]:
        """Open the SSE stream and yield typed events. Reconnects on
        non-terminal disconnects via ``Last-Event-ID`` + ``?lastSeq=``."""
        last_seq = 0
        # Tool dispatch happens off-thread so the stream consumer keeps reading.
        with ThreadPoolExecutor(max_workers=4, thread_name_prefix="mantyx-tool") as pool:
            while True:
                terminal_seen = False
                url = self._absolute_url(f"/agent-runs/{_quote(run_id)}/stream")
                params: dict[str, Any] = {}
                if last_seq > 0:
                    params["lastSeq"] = last_seq
                headers = self._auth_headers()
                headers["Accept"] = "text/event-stream"
                if last_seq > 0:
                    headers["Last-Event-ID"] = str(last_seq)
                try:
                    with self._http.stream(
                        "GET", url, params=params, headers=headers, timeout=None
                    ) as resp:
                        if resp.status_code != 200:
                            self._raise_for_status(resp)
                        for sse_ev in iter_sse(resp.iter_bytes()):
                            ev = _to_run_event(sse_ev, last_seq)
                            if ev.seq > last_seq:
                                last_seq = ev.seq
                            yield ev
                            if ev.type == "local_tool_call":
                                pool.submit(self._dispatch_local_tool, run_id, ev, handlers)
                            if ev.type in ("result", "error", "cancelled"):
                                terminal_seen = True
                                break
                except httpx.HTTPError:  # network blip — retry
                    if terminal_seen:
                        return
                    time.sleep(0.5)
                    continue
                if terminal_seen:
                    return
                # Stream closed without a terminal event — reconnect.
                continue

    def _dispatch_local_tool(
        self,
        run_id: str,
        ev: RunEvent,
        handlers: _LocalHandlers,
    ) -> None:
        name = str(ev.data.get("name") or "")
        tool_use_id = str(ev.data.get("toolUseId") or "")
        if not tool_use_id:
            return
        kind = str(ev.data.get("kind") or "local")
        try:
            if kind == "a2a_local":
                a2a = handlers.a2a_tools.get(name)
                if a2a is None:
                    self._post_tool_result(
                        run_id,
                        tool_use_id,
                        error=f"No local A2A handler registered for tool {name!r}",
                    )
                    return
                args = ev.data.get("args") or {}
                message = ""
                if isinstance(args, dict):
                    raw_msg = args.get("message")
                    message = raw_msg if isinstance(raw_msg, str) else ""
                text = sync_call_a2a(a2a, message, http=self._http)
                self._post_tool_result(run_id, tool_use_id, result=text)
                return
            if kind == "mcp_local":
                server_name = str(ev.data.get("mcpServer") or "")
                tool_name = str(ev.data.get("mcpToolName") or "")
                server = handlers.mcp_servers.get(server_name)
                if server is None or server._resolved is None or server._resolved.call_sync is None:
                    self._post_tool_result(
                        run_id,
                        tool_use_id,
                        error=f"No local MCP server registered as {server_name!r}",
                    )
                    return
                # The wire-prefixed tool name (`<server>_<tool>`) is what the
                # model sees; the upstream MCP server uses the bare name.
                # Strip the prefix before forwarding to `tools/call`.
                upstream_name = (
                    tool_name[len(server_name) + 1 :]
                    if tool_name.startswith(f"{server_name}_")
                    else tool_name
                )
                args_in = ev.data.get("args") or ev.data.get("input") or {}
                args_dict: dict[str, Any] = (
                    cast(dict[str, Any], args_in) if isinstance(args_in, dict) else {}
                )
                text = server._resolved.call_sync(upstream_name, args_dict)
                self._post_tool_result(run_id, tool_use_id, result=text)
                return
            handler = handlers.local_tools.get(name)
            if handler is None:
                self._post_tool_result(
                    run_id, tool_use_id, error=f"No local handler registered for tool {name!r}"
                )
                return
            args = parse_args_with_pydantic(
                handler.parameters,
                cast(dict[str, Any] | None, ev.data.get("args") or ev.data.get("input")),
            )
            from .tools import call_handler_sync

            out = call_handler_sync(handler.execute, args)
            text = out if isinstance(out, str) else json.dumps(out)
            self._post_tool_result(run_id, tool_use_id, result=text)
        except Exception as e:
            self._post_tool_result(
                run_id,
                tool_use_id,
                error=MantyxToolError(_describe_handler(ev, name), str(e)).message,
            )

    def _post_tool_result(
        self,
        run_id: str,
        tool_use_id: str,
        *,
        result: str | None = None,
        error: str | None = None,
    ) -> None:
        body: dict[str, Any] = {"toolUseId": tool_use_id}
        if result is not None:
            body["result"] = result
        if error is not None:
            body["error"] = error
        try:
            self._request("POST", f"/agent-runs/{_quote(run_id)}/tool-results", body)
        except MantyxError:
            # The server will time-out the tool-use and surface the right
            # terminal event on the SSE stream; logging is enough here.
            pass

    # --------------------------------------------------------------- HTTP

    def _absolute_url(self, path: str) -> str:
        return f"{self.base_url}/api/v1/workspaces/{_quote(self.workspace_slug)}{path}"

    def _auth_headers(self) -> dict[str, str]:
        return {"Authorization": f"Bearer {self.api_key}"}

    def _request(
        self,
        method: str,
        path: str,
        body: Mapping[str, Any] | None = None,
    ) -> dict[str, Any] | None:
        url = self._absolute_url(path)
        headers = self._auth_headers()
        headers["Accept"] = "application/json"
        request_kwargs: dict[str, Any] = {"method": method, "url": url, "headers": headers}
        if body is not None:
            request_kwargs["json"] = body
            headers["Content-Type"] = "application/json"
        try:
            resp = self._http.request(**request_kwargs)
        except httpx.HTTPError as exc:
            raise MantyxNetworkError(str(exc), cause=exc) from exc
        if resp.status_code >= 400:
            self._raise_for_status(resp)
        text = resp.text
        if not text:
            return None
        try:
            data = resp.json()
        except ValueError as exc:
            raise MantyxError(f"Failed to parse JSON response: {exc}") from exc
        if isinstance(data, dict):
            return cast(dict[str, Any], data)
        return None

    def _raise_for_status(self, resp: httpx.Response) -> None:
        body: dict[str, Any] = {}
        try:
            body = resp.json() or {}
        except Exception:
            pass
        message = str(body.get("error") or body.get("message") or f"HTTP {resp.status_code}")
        code = str(body.get("code") or body.get("error") or f"http_{resp.status_code}")
        hint_raw = body.get("hint")
        hint = hint_raw if isinstance(hint_raw, str) else None
        if resp.status_code == 401:
            raise MantyxAuthError(message)
        raise MantyxError(message, code=code, status=resp.status_code, hint=hint)


# -------------------------------------------------------------------- Session


class AgentSession:
    """Multi-turn conversation handle. The server owns the message history;
    the SDK holds the local-tool handlers in memory."""

    def __init__(
        self,
        client: MantyxClient,
        *,
        id: str,
        handlers: _LocalHandlers,
        tools_for_resume: list[ToolRef] | None = None,
    ) -> None:
        self.client = client
        self.id = id
        self._handlers = handlers
        self._tools_for_resume = tools_for_resume

    def send(
        self,
        prompt: str,
        *,
        metadata: Mapping[str, str] | None = None,
        reasoning_level: ReasoningLevel | None = None,
        output_schema: OutputSchema | Mapping[str, Any] | None = None,
        loop_detection: LoopDetection | Mapping[str, Any] | bool | None = _UNSET,
        tool_budgets: ToolBudgets | Mapping[str, Mapping[str, Any]] | None = _UNSET,
        on_assistant_delta: Callable[[str], None] | None = None,
        on_event: Callable[[RunEvent], None] | None = None,
    ) -> RunResult:
        body = self._build_message_body(
            prompt,
            metadata=metadata,
            reasoning_level=reasoning_level,
            output_schema=output_schema,
            loop_detection=loop_detection,
            tool_budgets=tool_budgets,
        )
        created = (
            self.client._request("POST", f"/agent-sessions/{_quote(self.id)}/messages", body) or {}
        )
        run_id = str(created.get("runId") or "")
        if not run_id:
            raise MantyxError("server did not return a runId")
        return self.client._drive_run(
            run_id=run_id,
            handlers=self._handlers,
            on_assistant_delta=on_assistant_delta,
            on_event=on_event,
        )

    def stream(
        self,
        prompt: str,
        *,
        metadata: Mapping[str, str] | None = None,
        reasoning_level: ReasoningLevel | None = None,
        output_schema: OutputSchema | Mapping[str, Any] | None = None,
        loop_detection: LoopDetection | Mapping[str, Any] | bool | None = _UNSET,
        tool_budgets: ToolBudgets | Mapping[str, Mapping[str, Any]] | None = _UNSET,
    ) -> Iterator[RunEvent]:
        body = self._build_message_body(
            prompt,
            metadata=metadata,
            reasoning_level=reasoning_level,
            output_schema=output_schema,
            loop_detection=loop_detection,
            tool_budgets=tool_budgets,
        )
        created = (
            self.client._request("POST", f"/agent-sessions/{_quote(self.id)}/messages", body) or {}
        )
        run_id = str(created.get("runId") or "")
        if not run_id:
            raise MantyxError("server did not return a runId")
        return self.client._stream_events(run_id, self._handlers)

    def _build_message_body(
        self,
        prompt: str,
        *,
        metadata: Mapping[str, str] | None,
        reasoning_level: ReasoningLevel | None,
        output_schema: OutputSchema | Mapping[str, Any] | None = None,
        loop_detection: LoopDetection | Mapping[str, Any] | bool | None = _UNSET,
        tool_budgets: ToolBudgets | Mapping[str, Mapping[str, Any]] | None = _UNSET,
    ) -> dict[str, Any]:
        body: dict[str, Any] = {"prompt": prompt}
        if self._tools_for_resume:
            body["tools"] = serialize_tool_refs(self._tools_for_resume)
        if metadata:
            body["metadata"] = dict(metadata)
        normalized = normalize_reasoning_level(reasoning_level)
        if normalized is not None:
            body["reasoningLevel"] = normalized
        normalized_schema = normalize_output_schema(output_schema)
        if normalized_schema is not None:
            body["outputSchema"] = normalized_schema
        if loop_detection is not _UNSET:
            normalized_loop = normalize_loop_detection(loop_detection)
            if normalized_loop is not None:
                body["loopDetection"] = normalized_loop
        if tool_budgets is not _UNSET:
            normalized_budgets = normalize_tool_budgets(tool_budgets)
            if normalized_budgets is not None:
                body["toolBudgets"] = normalized_budgets
        return body

    def history(self) -> list[dict[str, str]]:
        info = self.client.get_session_info(self.id)
        return info.messages

    def info(self) -> SessionInfo:
        return self.client.get_session_info(self.id)

    def end(self) -> None:
        try:
            self.client.end_session(self.id)
        finally:
            # Close any MCP transports the session opened.
            sync_close_mcp_refs(self._tools_for_resume)


# -------------------------------------------------------------------- Helpers


def _quote(s: str) -> str:
    """Tight URL-path escape; keeps simple alphanumerics intact, percent-
    encodes anything else (so workspace slugs / ids round-trip safely)."""
    out: list[str] = []
    for ch in s:
        cp = ord(ch)
        if (
            (ord("a") <= cp <= ord("z"))
            or (ord("A") <= cp <= ord("Z"))
            or (ord("0") <= cp <= ord("9"))
            or ch in "-_."
        ):
            out.append(ch)
        else:
            for b in ch.encode("utf-8"):
                out.append(f"%{b:02X}")
    return "".join(out)


def _serialize_agent_spec(
    *,
    agent_id: str | None,
    system_prompt: str | None,
    model_id: str | None,
    name: str | None,
    tools: list[ToolRef] | None,
    reasoning_level: ReasoningLevel | None,
    output_schema: OutputSchema | Mapping[str, Any] | None,
    loop_detection: LoopDetection | Mapping[str, Any] | bool | None = _UNSET,
    tool_budgets: ToolBudgets | Mapping[str, Mapping[str, Any]] | None = _UNSET,
    budgets: Mapping[str, Any] | None,
    metadata: Mapping[str, str] | None,
) -> dict[str, Any]:
    if not agent_id and not system_prompt:
        raise MantyxError("Either agent_id or system_prompt is required")
    body: dict[str, Any] = {"tools": serialize_tool_refs(tools)}
    if system_prompt:
        body["systemPrompt"] = system_prompt
    if agent_id:
        body["agentId"] = agent_id
    if name:
        body["name"] = name
    if model_id:
        body["modelId"] = model_id
    normalized_level = normalize_reasoning_level(reasoning_level)
    if normalized_level is not None:
        body["reasoningLevel"] = normalized_level
    normalized_schema = normalize_output_schema(output_schema)
    if normalized_schema is not None:
        body["outputSchema"] = normalized_schema
    if loop_detection is not _UNSET:
        normalized_loop = normalize_loop_detection(loop_detection)
        if normalized_loop is not None:
            body["loopDetection"] = normalized_loop
    if tool_budgets is not _UNSET:
        normalized_budgets = normalize_tool_budgets(tool_budgets)
        if normalized_budgets is not None:
            body["toolBudgets"] = normalized_budgets
    if budgets:
        body["budgets"] = dict(budgets)
    if metadata:
        body["metadata"] = dict(metadata)
    return body


def _describe_handler(ev: RunEvent, fallback: str) -> str:
    if str(ev.data.get("kind") or "") == "mcp_local":
        s = str(ev.data.get("mcpServer") or "")
        t = str(ev.data.get("mcpToolName") or "")
        if s and t:
            return f"{s}/{t}"
    return fallback


def _to_run_event(sse_ev: SseEvent, last_seq: int) -> RunEvent:
    data: dict[str, Any] = {}
    if sse_ev.data:
        try:
            parsed = json.loads(sse_ev.data)
            if isinstance(parsed, dict):
                data = parsed
        except json.JSONDecodeError:
            data = {}
    type_ = sse_ev.event or (data.get("type") if isinstance(data.get("type"), str) else "message")
    seq_raw = data.get("seq")
    seq = int(seq_raw) if isinstance(seq_raw, (int, float)) else last_seq
    payload = cast(dict[str, Any], data.get("data") if isinstance(data.get("data"), dict) else data)
    return RunEvent(seq=seq, type=str(type_), data=payload)


def _parse_model_catalog(body: Mapping[str, Any]) -> ModelCatalog:
    raw_models = body.get("models") if isinstance(body.get("models"), list) else []
    models: list[ModelInfo] = []
    for m in cast(Iterable[Any], raw_models):
        if not isinstance(m, dict):
            continue
        pricing = None
        p = m.get("pricing")
        if isinstance(p, dict):
            pricing = PricingInfo(
                inputPer1MUsd=_as_optional_float(p.get("inputPer1MUsd")),
                outputPer1MUsd=_as_optional_float(p.get("outputPer1MUsd")),
                cacheReadPer1MUsd=_as_optional_float(p.get("cacheReadPer1MUsd")),
            )
        ctx_raw = m.get("contextWindowTokens")
        ctx = int(ctx_raw) if isinstance(ctx_raw, (int, float)) else None
        models.append(
            ModelInfo(
                id=str(m.get("id") or ""),
                label=str(m.get("label") or ""),
                provider=str(m.get("provider") or ""),
                vendor_model_id=str(m.get("vendorModelId") or ""),
                source=str(m.get("source") or ""),
                context_window_tokens=ctx,
                pricing=pricing,
            )
        )
    default_raw = body.get("defaultModelId")
    return ModelCatalog(
        models=models,
        default_model_id=default_raw if isinstance(default_raw, str) else None,
    )


def _parse_session_info(body: Mapping[str, Any]) -> SessionInfo:
    msgs_raw = body.get("messages") if isinstance(body.get("messages"), list) else []
    messages: list[dict[str, str]] = []
    for m in cast(Iterable[Any], msgs_raw):
        if isinstance(m, dict):
            messages.append(
                {"role": str(m.get("role") or ""), "content": str(m.get("content") or "")}
            )
    metadata_raw = body.get("metadata")
    metadata: dict[str, str] = {}
    if isinstance(metadata_raw, dict):
        for k, v in metadata_raw.items():
            metadata[str(k)] = str(v)
    return SessionInfo(
        id=str(body.get("id") or ""),
        name=str(body.get("name") or ""),
        status=str(body.get("status") or ""),
        created_at=str(body.get("createdAt") or ""),
        last_used_at=str(body.get("lastUsedAt") or ""),
        ended_at=cast(str | None, body.get("endedAt")),
        agent_spec=cast(dict[str, Any], body.get("agentSpec") or {}),
        messages=messages,
        metadata=metadata,
    )


def _as_optional_float(v: Any) -> float | None:
    if isinstance(v, (int, float)):
        return float(v)
    return None


def parse_run_output(
    result: RunResult,
    validator: Callable[[Any], Any] | None = None,
) -> Any:
    """Parse the terminal text of a :class:`RunResult` as JSON.

    When the run was submitted with ``output_schema``, MANTYX (via the LLM
    provider) guarantees the reply parses as JSON in the *vast* majority
    of cases. Transient model errors (refusal text, truncation under
    ``max_tokens`` pressure, exotic Unicode) can still produce strings
    that fail to ``json.loads`` in rare edge cases — this helper
    centralises that brittle step and surfaces a typed
    :class:`MantyxParseError` on failure with the original text preserved
    on ``err.text``.

    Pass an optional ``validator`` (a Pydantic ``model_validate``, an
    ``ajv.compile``-style validator, or any callable) to re-validate
    against your source-of-truth schema. Its return value is forwarded;
    any exception is wrapped in :class:`MantyxParseError`.

    Example::

        from pydantic import BaseModel
        from mantyx import parse_run_output

        class WeatherReport(BaseModel):
            city: str
            temperature_c: float

        result = client.run_agent(
            system_prompt="...",
            prompt="What's the weather in SF?",
            output_schema={"name": "weather_report", "schema": WEATHER_JSON_SCHEMA},
        )
        report = parse_run_output(result, WeatherReport.model_validate)
    """
    try:
        parsed = json.loads(result.text)
    except json.JSONDecodeError as exc:
        raise MantyxParseError(
            f"Run {result.run_id} returned non-JSON text; cannot satisfy output_schema",
            text=result.text,
            cause=exc,
        ) from exc
    if validator is None:
        return parsed
    try:
        return validator(parsed)
    except Exception as exc:
        raise MantyxParseError(
            f"Run {result.run_id} output failed validation: {exc}",
            text=result.text,
            cause=exc,
        ) from exc


__all__ = [
    "DEFAULT_BASE_URL",
    "AgentSession",
    "MantyxClient",
    "ModelCatalog",
    "ModelInfo",
    "PricingInfo",
    "RunEvent",
    "RunResult",
    "SessionInfo",
    "parse_run_output",
]
