"""Asynchronous MANTYX client (httpx.AsyncClient).

Mirrors :mod:`mantyx.client` but exposes coroutines and async iterators.
"""

from __future__ import annotations

import asyncio
import json
from collections.abc import AsyncIterator, Callable, Mapping, Sequence
from typing import (
    Any,
    cast,
)

import httpx

from ._local_resolver import (
    async_call_a2a,
    async_close_mcp_refs,
    async_resolve_local_refs,
)
from ._schema import parse_args_with_pydantic
from ._version import SDK_VERSION
from .client import (
    DEFAULT_BASE_URL,
    DEFAULT_TIMEOUT_S,
    ModelCatalog,
    RunEvent,
    RunResult,
    SessionInfo,
    _describe_handler,
    _parse_model_catalog,
    _parse_session_info,
    _quote,
    _serialize_agent_spec,
    _to_run_event,
)
from .errors import (
    MantyxAuthError,
    MantyxError,
    MantyxNetworkError,
    MantyxRunError,
    MantyxToolError,
)
from .sse import aiter_sse
from .tools import (
    OutputSchema,
    ReasoningLevel,
    ToolRef,
    _LocalHandlers,
    collect_local_handlers,
    maybe_await,
    normalize_output_schema,
    normalize_reasoning_level,
    serialize_tool_refs,
)


class AsyncMantyxClient:
    """Asynchronous MANTYX client.

    Example:
        >>> async with AsyncMantyxClient(api_key="...", workspace_slug="acme") as client:
        ...     result = await client.run_agent(
        ...         system_prompt="...",
        ...         prompt="hi",
        ...     )
        ...     print(result.text)
    """

    def __init__(
        self,
        *,
        api_key: str,
        workspace_slug: str,
        base_url: str = DEFAULT_BASE_URL,
        timeout: float = DEFAULT_TIMEOUT_S,
        http_client: httpx.AsyncClient | None = None,
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
        self._http = http_client or httpx.AsyncClient(
            timeout=httpx.Timeout(timeout, connect=10.0, read=None),
            headers={"User-Agent": f"mantyx-sdk-python/{SDK_VERSION}"},
        )

    # ------------------------------------------------------------------ ctx

    async def __aenter__(self) -> AsyncMantyxClient:
        return self

    async def __aexit__(self, *exc: Any) -> None:
        await self.aclose()

    async def aclose(self) -> None:
        if self._owns_http:
            await self._http.aclose()

    # --------------------------------------------------------------- Models

    async def list_models(self) -> ModelCatalog:
        body = await self._request("GET", "/models")
        return _parse_model_catalog(body or {})

    # --------------------------------------------------------------- Runs

    async def run_agent(
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
        budgets: Mapping[str, Any] | None = None,
        metadata: Mapping[str, str] | None = None,
        on_assistant_delta: Callable[[str], Any] | None = None,
        on_event: Callable[[RunEvent], Any] | None = None,
    ) -> RunResult:
        tools_list: list[ToolRef] | None = list(tools) if tools else None
        await async_resolve_local_refs(tools_list, http=self._http)
        try:
            body = _serialize_agent_spec(
                agent_id=agent_id,
                system_prompt=system_prompt,
                model_id=model_id,
                name=name,
                tools=tools_list,
                reasoning_level=reasoning_level,
                output_schema=output_schema,
                budgets=budgets,
                metadata=metadata,
            )
            if prompt is not None:
                body["prompt"] = prompt
            if messages is not None:
                body["messages"] = list(messages)
            created = await self._request("POST", "/agent-runs", body)
            run_id = str((created or {}).get("runId") or "")
            if not run_id:
                raise MantyxError("server did not return a runId")
            handlers = collect_local_handlers(tools_list)
            return await self._drive_run(
                run_id=run_id,
                handlers=handlers,
                on_assistant_delta=on_assistant_delta,
                on_event=on_event,
            )
        finally:
            await async_close_mcp_refs(tools_list)

    async def stream_agent(
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
        budgets: Mapping[str, Any] | None = None,
        metadata: Mapping[str, str] | None = None,
    ) -> AsyncIterator[RunEvent]:
        """Stream events from a one-shot run as they arrive.

        This is an async generator: ``async for event in client.stream_agent(...)``.
        """
        tools_list: list[ToolRef] | None = list(tools) if tools else None
        await async_resolve_local_refs(tools_list, http=self._http)
        try:
            body = _serialize_agent_spec(
                agent_id=agent_id,
                system_prompt=system_prompt,
                model_id=model_id,
                name=name,
                tools=tools_list,
                reasoning_level=reasoning_level,
                output_schema=output_schema,
                budgets=budgets,
                metadata=metadata,
            )
            if prompt is not None:
                body["prompt"] = prompt
            if messages is not None:
                body["messages"] = list(messages)
            created = await self._request("POST", "/agent-runs", body)
            run_id = str((created or {}).get("runId") or "")
            if not run_id:
                raise MantyxError("server did not return a runId")
            handlers = collect_local_handlers(tools_list)
            async for ev in self._stream_events(run_id, handlers):
                yield ev
        finally:
            await async_close_mcp_refs(tools_list)

    async def cancel_run(self, run_id: str) -> None:
        await self._request("POST", f"/agent-runs/{_quote(run_id)}/cancel")

    # ----------------------------------------------------------- Sessions

    async def create_session(
        self,
        *,
        system_prompt: str | None = None,
        agent_id: str | None = None,
        model_id: str | None = None,
        name: str | None = None,
        tools: Sequence[ToolRef] | None = None,
        reasoning_level: ReasoningLevel | None = None,
        output_schema: OutputSchema | Mapping[str, Any] | None = None,
        budgets: Mapping[str, Any] | None = None,
        metadata: Mapping[str, str] | None = None,
    ) -> AsyncAgentSession:
        tools_list: list[ToolRef] | None = list(tools) if tools else None
        await async_resolve_local_refs(tools_list, http=self._http)
        try:
            body = _serialize_agent_spec(
                agent_id=agent_id,
                system_prompt=system_prompt,
                model_id=model_id,
                name=name,
                tools=tools_list,
                reasoning_level=reasoning_level,
                output_schema=output_schema,
                budgets=budgets,
                metadata=metadata,
            )
            created = await self._request("POST", "/agent-sessions", body) or {}
        except Exception:
            await async_close_mcp_refs(tools_list)
            raise
        session_id = str(created.get("sessionId") or "")
        if not session_id:
            await async_close_mcp_refs(tools_list)
            raise MantyxError("server did not return a sessionId")
        handlers = collect_local_handlers(tools_list)
        return AsyncAgentSession(
            self, id=session_id, handlers=handlers, tools_for_resume=tools_list
        )

    async def resume_session(
        self,
        session_id: str,
        *,
        tools: Sequence[ToolRef] | None = None,
    ) -> AsyncAgentSession:
        await self.get_session_info(session_id)
        tools_list: list[ToolRef] | None = list(tools) if tools else None
        if tools_list:
            await async_resolve_local_refs(tools_list, http=self._http)
        handlers = collect_local_handlers(tools_list)
        return AsyncAgentSession(
            self,
            id=session_id,
            handlers=handlers,
            tools_for_resume=tools_list,
        )

    async def end_session(self, session_id: str) -> None:
        await self._request("DELETE", f"/agent-sessions/{_quote(session_id)}")

    async def get_session_info(self, session_id: str) -> SessionInfo:
        body = await self._request("GET", f"/agent-sessions/{_quote(session_id)}") or {}
        return _parse_session_info(body)

    # ------------------------------------------------------------ Internals

    async def _drive_run(
        self,
        *,
        run_id: str,
        handlers: _LocalHandlers,
        on_assistant_delta: Callable[[str], Any] | None = None,
        on_event: Callable[[RunEvent], Any] | None = None,
    ) -> RunResult:
        collected: list[RunEvent] = []
        final_text = ""
        async for ev in self._stream_events(run_id, handlers):
            collected.append(ev)
            if on_event is not None:
                await maybe_await(on_event(ev))
            if ev.type == "assistant_delta" and on_assistant_delta is not None:
                t = ev.data.get("text")
                if isinstance(t, str):
                    await maybe_await(on_assistant_delta(t))
            if ev.type == "result":
                subtype = str(ev.data.get("subtype") or "")
                if subtype == "success":
                    txt = ev.data.get("text")
                    final_text = txt if isinstance(txt, str) else ""
                else:
                    msg = ev.data.get("error") or subtype or "run failed"
                    raise MantyxRunError(run_id, subtype or "error", str(msg))
            elif ev.type == "error":
                code = str(ev.data.get("code") or "error")
                raise MantyxRunError(run_id, code, str(ev.data.get("error") or "error"))
            elif ev.type == "cancelled":
                raise MantyxRunError(run_id, "cancelled", "Run was cancelled")
        return RunResult(run_id=run_id, text=final_text, events=collected)

    async def _stream_events(
        self,
        run_id: str,
        handlers: _LocalHandlers,
    ) -> AsyncIterator[RunEvent]:
        last_seq = 0
        background: list[asyncio.Task[Any]] = []
        try:
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
                    async with self._http.stream(
                        "GET", url, params=params, headers=headers, timeout=None
                    ) as resp:
                        if resp.status_code != 200:
                            await self._raise_for_status(resp)
                        async for sse_ev in aiter_sse(resp.aiter_bytes()):
                            ev = _to_run_event(sse_ev, last_seq)
                            if ev.seq > last_seq:
                                last_seq = ev.seq
                            yield ev
                            if ev.type == "local_tool_call":
                                background.append(
                                    asyncio.create_task(
                                        self._dispatch_local_tool(run_id, ev, handlers)
                                    )
                                )
                            if ev.type in ("result", "error", "cancelled"):
                                terminal_seen = True
                                break
                except httpx.HTTPError:
                    if terminal_seen:
                        return
                    await asyncio.sleep(0.5)
                    continue
                if terminal_seen:
                    return
                continue
        finally:
            # Best-effort wait for in-flight tool dispatches so their results
            # are POSTed before the iterator returns.
            if background:
                await asyncio.gather(*background, return_exceptions=True)

    async def _dispatch_local_tool(
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
                    await self._post_tool_result(
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
                text = await async_call_a2a(a2a, message, http=self._http)
                await self._post_tool_result(run_id, tool_use_id, result=text)
                return
            if kind == "mcp_local":
                server_name = str(ev.data.get("mcpServer") or "")
                tool_name = str(ev.data.get("mcpToolName") or "")
                server = handlers.mcp_servers.get(server_name)
                if server is None or server._resolved is None:
                    await self._post_tool_result(
                        run_id,
                        tool_use_id,
                        error=f"No local MCP server registered as {server_name!r}",
                    )
                    return
                upstream_name = (
                    tool_name[len(server_name) + 1 :]
                    if tool_name.startswith(f"{server_name}_")
                    else tool_name
                )
                args_in = ev.data.get("args") or ev.data.get("input") or {}
                args_dict: dict[str, Any] = (
                    cast(dict[str, Any], args_in) if isinstance(args_in, dict) else {}
                )
                text = await server._resolved.call_async(upstream_name, args_dict)
                await self._post_tool_result(run_id, tool_use_id, result=text)
                return
            handler = handlers.local_tools.get(name)
            if handler is None:
                await self._post_tool_result(
                    run_id,
                    tool_use_id,
                    error=f"No local handler registered for tool {name!r}",
                )
                return
            args = parse_args_with_pydantic(
                handler.parameters,
                cast(dict[str, Any] | None, ev.data.get("args") or ev.data.get("input")),
            )
            out = await maybe_await(handler.execute(args))
            text = out if isinstance(out, str) else json.dumps(out)
            await self._post_tool_result(run_id, tool_use_id, result=text)
        except Exception as e:
            await self._post_tool_result(
                run_id,
                tool_use_id,
                error=MantyxToolError(_describe_handler(ev, name), str(e)).message,
            )

    async def _post_tool_result(
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
            await self._request("POST", f"/agent-runs/{_quote(run_id)}/tool-results", body)
        except MantyxError:
            pass

    # ---------------------------------------------------------------- HTTP

    def _absolute_url(self, path: str) -> str:
        return f"{self.base_url}/api/v1/workspaces/{_quote(self.workspace_slug)}{path}"

    def _auth_headers(self) -> dict[str, str]:
        return {"Authorization": f"Bearer {self.api_key}"}

    async def _request(
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
            resp = await self._http.request(**request_kwargs)
        except httpx.HTTPError as exc:
            raise MantyxNetworkError(str(exc), cause=exc) from exc
        if resp.status_code >= 400:
            await self._raise_for_status(resp)
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

    async def _raise_for_status(self, resp: httpx.Response) -> None:
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


# ----------------------------------------------------------------- Session


class AsyncAgentSession:
    """Async multi-turn conversation handle."""

    def __init__(
        self,
        client: AsyncMantyxClient,
        *,
        id: str,
        handlers: _LocalHandlers,
        tools_for_resume: list[ToolRef] | None = None,
    ) -> None:
        self.client = client
        self.id = id
        self._handlers = handlers
        self._tools_for_resume = tools_for_resume

    async def send(
        self,
        prompt: str,
        *,
        metadata: Mapping[str, str] | None = None,
        reasoning_level: ReasoningLevel | None = None,
        output_schema: OutputSchema | Mapping[str, Any] | None = None,
        on_assistant_delta: Callable[[str], Any] | None = None,
        on_event: Callable[[RunEvent], Any] | None = None,
    ) -> RunResult:
        body = self._build_message_body(
            prompt,
            metadata=metadata,
            reasoning_level=reasoning_level,
            output_schema=output_schema,
        )
        created = (
            await self.client._request("POST", f"/agent-sessions/{_quote(self.id)}/messages", body)
            or {}
        )
        run_id = str(created.get("runId") or "")
        if not run_id:
            raise MantyxError("server did not return a runId")
        return await self.client._drive_run(
            run_id=run_id,
            handlers=self._handlers,
            on_assistant_delta=on_assistant_delta,
            on_event=on_event,
        )

    async def stream(
        self,
        prompt: str,
        *,
        metadata: Mapping[str, str] | None = None,
        reasoning_level: ReasoningLevel | None = None,
        output_schema: OutputSchema | Mapping[str, Any] | None = None,
    ) -> AsyncIterator[RunEvent]:
        """Stream events from a session turn as they arrive.

        Async generator: ``async for event in session.stream("hi"):``.
        """
        body = self._build_message_body(
            prompt,
            metadata=metadata,
            reasoning_level=reasoning_level,
            output_schema=output_schema,
        )
        created = (
            await self.client._request("POST", f"/agent-sessions/{_quote(self.id)}/messages", body)
            or {}
        )
        run_id = str(created.get("runId") or "")
        if not run_id:
            raise MantyxError("server did not return a runId")
        async for ev in self.client._stream_events(run_id, self._handlers):
            yield ev

    def _build_message_body(
        self,
        prompt: str,
        *,
        metadata: Mapping[str, str] | None,
        reasoning_level: ReasoningLevel | None,
        output_schema: OutputSchema | Mapping[str, Any] | None = None,
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
        return body

    async def history(self) -> list[dict[str, str]]:
        info = await self.client.get_session_info(self.id)
        return info.messages

    async def info(self) -> SessionInfo:
        return await self.client.get_session_info(self.id)

    async def end(self) -> None:
        try:
            await self.client.end_session(self.id)
        finally:
            await async_close_mcp_refs(self._tools_for_resume)


__all__ = ["AsyncAgentSession", "AsyncMantyxClient"]
