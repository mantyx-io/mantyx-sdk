"""Resolution + dispatch for ``a2a_local`` and ``mcp_local`` tool refs.

The wire protocol requires the SDK to ship the *resolved* A2A Agent Card
and the *resolved* MCP ``Tool[]`` inline as part of the spec. Users only
give the SDK a URL (or stdio command); this module turns that into the
resolved snapshot at spec-submit time, and speaks A2A ``message/send`` /
MCP ``tools/call`` at invocation time.

This is the canonical async implementation used by :class:`AsyncMantyxClient`
directly, and wrapped via an ``anyio.from_thread.BlockingPortal`` by the
synchronous :class:`MantyxClient` so MCP's async-only client can be driven
from a sync caller.

Internal — not part of the public API.
"""

from __future__ import annotations

import json
import uuid
from contextlib import AsyncExitStack
from typing import Any

import httpx

from .tools import (
    LocalA2ATool,
    LocalMcpServer,
    ToolRef,
    _ResolvedMcpServer,
)

# ---------------------------------------------------------------------------
# A2A
# ---------------------------------------------------------------------------


async def _async_fetch_agent_card(
    client: httpx.AsyncClient,
    url: str,
    headers: dict[str, str] | None,
) -> dict[str, Any]:
    resp = await client.get(url, headers={"Accept": "application/json", **(headers or {})})
    if resp.status_code >= 300:
        raise ValueError(
            f"Agent Card fetch from {url} returned {resp.status_code} {resp.reason_phrase}"
        )
    try:
        body = resp.json()
    except ValueError as exc:
        raise ValueError(f"Agent Card at {url} did not return valid JSON") from exc
    if not isinstance(body, dict) or not isinstance(body.get("name"), str) or not body.get("name"):
        raise ValueError(f"Agent Card at {url} did not include the spec-required `name` field")
    return body


def _sync_fetch_agent_card(
    client: httpx.Client,
    url: str,
    headers: dict[str, str] | None,
) -> dict[str, Any]:
    resp = client.get(url, headers={"Accept": "application/json", **(headers or {})})
    if resp.status_code >= 300:
        raise ValueError(
            f"Agent Card fetch from {url} returned {resp.status_code} {resp.reason_phrase}"
        )
    try:
        body = resp.json()
    except ValueError as exc:
        raise ValueError(f"Agent Card at {url} did not return valid JSON") from exc
    if not isinstance(body, dict) or not isinstance(body.get("name"), str) or not body.get("name"):
        raise ValueError(f"Agent Card at {url} did not include the spec-required `name` field")
    return body


def _build_a2a_message_send(message: str) -> dict[str, Any]:
    return {
        "jsonrpc": "2.0",
        "id": uuid.uuid4().hex,
        "method": "message/send",
        "params": {
            "message": {
                "kind": "message",
                "role": "user",
                "messageId": uuid.uuid4().hex,
                "parts": [{"kind": "text", "text": message}],
            }
        },
    }


def _extract_a2a_reply_text(result: Any) -> str:
    """Pull a single text string out of an A2A `message/send` result."""
    if result is None:
        return ""
    if isinstance(result, str):
        return result
    if not isinstance(result, dict):
        return json.dumps(result)
    parts = result.get("parts")
    if isinstance(parts, list):
        text = _text_from_parts(parts)
        if text:
            return text
    status = result.get("status")
    if isinstance(status, dict):
        status_message = status.get("message")
        if isinstance(status_message, dict) and isinstance(status_message.get("parts"), list):
            text = _text_from_parts(status_message["parts"])
            if text:
                return text
    artifacts = result.get("artifacts")
    if isinstance(artifacts, list) and artifacts:
        last = artifacts[-1]
        if isinstance(last, dict) and isinstance(last.get("parts"), list):
            text = _text_from_parts(last["parts"])
            if text:
                return text
    return json.dumps(result)


def _text_from_parts(parts: list[Any]) -> str:
    out: list[str] = []
    for part in parts:
        if not isinstance(part, dict):
            continue
        kind = part.get("kind") or part.get("type")
        if kind == "text" and isinstance(part.get("text"), str):
            out.append(str(part["text"]))
    return "\n".join(out)


async def async_call_a2a(
    tool: LocalA2ATool,
    message: str,
    *,
    http: httpx.AsyncClient,
) -> str:
    """POST `message/send` to the resolved Agent Card URL and return the reply text."""
    if tool._resolved_card is None:
        raise ValueError(f"define_local_a2a({tool.name!r}): agent card has not been resolved")
    url = tool._resolved_card.get("url")
    if not isinstance(url, str) or not url:
        url = tool.agent_card_url
    body = _build_a2a_message_send(message)
    resp = await http.post(
        url,
        json=body,
        headers={
            "Content-Type": "application/json",
            "Accept": "application/json",
            **(tool.headers or {}),
        },
    )
    if resp.status_code >= 300:
        raise ValueError(
            f"A2A message/send to {url} returned {resp.status_code} {resp.reason_phrase}"
        )
    payload = resp.json()
    if isinstance(payload, dict) and isinstance(payload.get("error"), dict):
        err = payload["error"]
        raise ValueError(f"A2A peer reported error {err.get('code')!r}: {err.get('message')!r}")
    return _extract_a2a_reply_text(payload.get("result") if isinstance(payload, dict) else None)


def sync_call_a2a(
    tool: LocalA2ATool,
    message: str,
    *,
    http: httpx.Client,
) -> str:
    """Synchronous twin of :func:`async_call_a2a`."""
    if tool._resolved_card is None:
        raise ValueError(f"define_local_a2a({tool.name!r}): agent card has not been resolved")
    url = tool._resolved_card.get("url")
    if not isinstance(url, str) or not url:
        url = tool.agent_card_url
    body = _build_a2a_message_send(message)
    resp = http.post(
        url,
        json=body,
        headers={
            "Content-Type": "application/json",
            "Accept": "application/json",
            **(tool.headers or {}),
        },
    )
    if resp.status_code >= 300:
        raise ValueError(
            f"A2A message/send to {url} returned {resp.status_code} {resp.reason_phrase}"
        )
    payload = resp.json()
    if isinstance(payload, dict) and isinstance(payload.get("error"), dict):
        err = payload["error"]
        raise ValueError(f"A2A peer reported error {err.get('code')!r}: {err.get('message')!r}")
    return _extract_a2a_reply_text(payload.get("result") if isinstance(payload, dict) else None)


# ---------------------------------------------------------------------------
# MCP
# ---------------------------------------------------------------------------


def _flatten_mcp_text(content: Any) -> str:
    if not isinstance(content, list):
        return ""
    out: list[str] = []
    for block in content:
        if isinstance(block, dict):
            if block.get("type") == "text" and isinstance(block.get("text"), str):
                out.append(str(block["text"]))
        else:
            # mcp.types.TextContent and friends
            block_type = getattr(block, "type", None)
            text = getattr(block, "text", None)
            if block_type == "text" and isinstance(text, str):
                out.append(text)
    return "\n".join(out)


async def _open_mcp_session(server: LocalMcpServer) -> tuple[Any, AsyncExitStack]:
    """Open the MCP transport + ``ClientSession`` for ``server``. Returns
    ``(session, exit_stack)`` — close the stack to tear everything down.
    """
    # Lazy imports so apps that never use mcp_local don't pay the import cost.
    from mcp import ClientSession
    from mcp.client.stdio import StdioServerParameters, stdio_client
    from mcp.client.streamable_http import streamablehttp_client

    stack = AsyncExitStack()
    if server.http is not None:
        client_kwargs: dict[str, Any] = {}
        if server.http.headers:
            client_kwargs["headers"] = dict(server.http.headers)
        ctx = streamablehttp_client(server.http.url, **client_kwargs)
        # streamablehttp_client may yield (read, write) or (read, write, get_session_id)
        result = await stack.enter_async_context(ctx)
        read, write = result[0], result[1]
    elif server.stdio is not None:
        params = StdioServerParameters(
            command=server.stdio.command,
            args=list(server.stdio.args or []),
            env=dict(server.stdio.env) if server.stdio.env else None,
            cwd=server.stdio.cwd,
        )
        ctx = stdio_client(params)
        # stdio_client may yield (read, write) — or, on some versions,
        # additional pid/etc fields. Accept either and only bind the first
        # two streams.
        result = await stack.enter_async_context(ctx)
        read, write = result[0], result[1]
    else:
        raise ValueError(f"define_local_mcp({server.name!r}): missing transport")
    session = await stack.enter_async_context(ClientSession(read, write))
    await session.initialize()
    return session, stack


async def _async_resolve_mcp(server: LocalMcpServer) -> _ResolvedMcpServer:
    """Open the MCP transport, snapshot ``server_info`` + ``tools/list``, and
    return a :class:`_ResolvedMcpServer` whose ``call_async`` invokes
    ``tools/call`` on the same live session and ``aclose`` tears it down.
    """
    session, stack = await _open_mcp_session(server)

    # Snapshot the implementation block + catalog before returning.
    server_info: dict[str, Any] = {"name": server.name}
    impl = getattr(getattr(session, "_received_initialization_result", None), "serverInfo", None)
    if impl is not None:
        server_info = {
            "name": getattr(impl, "name", server.name),
            **({"version": impl.version} if getattr(impl, "version", None) else {}),
        }

    listed = await session.list_tools()
    tools: list[dict[str, Any]] = []
    for tool in listed.tools:
        entry: dict[str, Any] = {
            "name": tool.name,
            "inputSchema": dict(getattr(tool, "inputSchema", None) or {"type": "object"}),
        }
        description = getattr(tool, "description", None)
        if isinstance(description, str) and description:
            entry["description"] = description
        annotations = getattr(tool, "annotations", None)
        if annotations is not None:
            try:
                entry["annotations"] = annotations.model_dump(exclude_none=True)
            except AttributeError:
                if isinstance(annotations, dict):
                    entry["annotations"] = dict(annotations)
        tools.append(entry)

    async def _call_async(tool_name: str, arguments: dict[str, Any]) -> str:
        result = await session.call_tool(tool_name, arguments=arguments or None)
        if getattr(result, "isError", False):
            text = _flatten_mcp_text(getattr(result, "content", None))
            raise RuntimeError(text or "MCP tool reported an error")
        return _flatten_mcp_text(getattr(result, "content", None))

    async def _aclose() -> None:
        try:
            await stack.aclose()
        except Exception:
            # Best-effort cleanup — we don't want close-time errors to mask
            # real run errors.
            pass

    return _ResolvedMcpServer(
        server_info=server_info,
        tools=tools,
        call_async=_call_async,
        aclose=_aclose,
    )


# ---------------------------------------------------------------------------
# Public resolver entry points
# ---------------------------------------------------------------------------


async def async_resolve_local_refs(
    tools: list[ToolRef] | None,
    *,
    http: httpx.AsyncClient,
) -> list[LocalMcpServer]:
    """Resolve every ``a2a_local`` (HTTP fetch) + ``mcp_local`` (transport
    open + ``Initialize`` + ``tools/list``) ref in ``tools``. Mutates each
    ref in place to attach the resolved snapshot.

    Returns the list of MCP servers that were *opened in this call*, so
    the caller can close them when the run / session ends. Refs whose
    snapshot was already populated are left untouched (no double-open).
    """
    if not tools:
        return []
    newly_opened: list[LocalMcpServer] = []
    for t in tools:
        if isinstance(t, LocalA2ATool) and t._resolved_card is None:
            t._resolved_card = await _async_fetch_agent_card(http, t.agent_card_url, t.headers)
        elif isinstance(t, LocalMcpServer) and t._resolved is None:
            t._resolved = await _async_resolve_mcp(t)
            newly_opened.append(t)
    return newly_opened


async def async_close_mcp_refs(tools: list[ToolRef] | None) -> None:
    """Best-effort: close every resolved MCP transport on ``tools``. Safe to
    call multiple times — each call clears the cache afterwards."""
    if not tools:
        return
    for t in tools:
        if not isinstance(t, LocalMcpServer):
            continue
        resolved = t._resolved
        if resolved is None:
            continue
        t._resolved = None
        await resolved.aclose()


# ---------------------------------------------------------------------------
# Sync portal — wraps async MCP code so the synchronous client can drive it
# ---------------------------------------------------------------------------


class _SyncMcpPortal:
    """Lazily-created ``anyio.BlockingPortal`` shared by every ``mcp_local``
    ref the synchronous client uses. Owns its own daemon thread / event
    loop; closed when the client is closed.

    This wrapper exists because the official MCP Python SDK is async-only:
    the only way to run an MCP ``ClientSession`` from synchronous code is
    to drive it on a background event loop. anyio's BlockingPortal gives
    us exactly that — a portal lets sync code call coroutine functions
    on a long-lived event loop in a daemon thread.
    """

    def __init__(self) -> None:
        self._portal: Any = None
        self._stack: Any = None

    def _ensure_started(self) -> Any:
        if self._portal is None:
            from anyio.from_thread import start_blocking_portal

            self._stack = start_blocking_portal()
            self._portal = self._stack.__enter__()
        return self._portal

    def resolve(self, server: LocalMcpServer) -> _ResolvedMcpServer:
        portal = self._ensure_started()
        resolved: _ResolvedMcpServer = portal.call(_async_resolve_mcp, server)

        # Wrap the async call/close hooks with sync shims that route
        # through the portal so dispatch can run synchronously.
        def _call_sync(tool_name: str, arguments: dict[str, Any]) -> str:
            return str(portal.call(resolved.call_async, tool_name, arguments))

        def _close_sync() -> None:
            try:
                portal.call(resolved.aclose)
            except Exception:
                pass

        resolved.call_sync = _call_sync
        resolved.close_sync = _close_sync
        return resolved

    def close(self) -> None:
        if self._stack is not None:
            stack, self._stack = self._stack, None
            self._portal = None
            try:
                stack.__exit__(None, None, None)
            except Exception:
                pass


def sync_resolve_local_refs(
    tools: list[ToolRef] | None,
    *,
    http: httpx.Client,
    portal: _SyncMcpPortal,
) -> list[LocalMcpServer]:
    """Synchronous variant. A2A is fetched directly via ``httpx.Client``;
    MCP work is dispatched onto ``portal``'s background event loop.
    """
    if not tools:
        return []
    newly_opened: list[LocalMcpServer] = []
    for t in tools:
        if isinstance(t, LocalA2ATool) and t._resolved_card is None:
            t._resolved_card = _sync_fetch_agent_card(http, t.agent_card_url, t.headers)
        elif isinstance(t, LocalMcpServer) and t._resolved is None:
            t._resolved = portal.resolve(t)
            newly_opened.append(t)
    return newly_opened


def sync_close_mcp_refs(tools: list[ToolRef] | None) -> None:
    if not tools:
        return
    for t in tools:
        if not isinstance(t, LocalMcpServer):
            continue
        resolved = t._resolved
        if resolved is None:
            continue
        t._resolved = None
        if resolved.close_sync is not None:
            resolved.close_sync()
