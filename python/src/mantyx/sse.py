"""Minimal Server-Sent Events parser.

Reads bytes from a transport (sync ``httpx.Response.iter_bytes`` or async
``httpx.Response.aiter_bytes``) and yields parsed events with ``id``,
``event`` and ``data`` fields. We deliberately keep this dependency-free
instead of pulling in ``httpx-sse`` so the SDK has the smallest possible
install footprint.

Reconnect/replay is handled at a higher layer (``client.py``) using
``Last-Event-ID`` plus a ``?lastSeq=`` query param.
"""

from __future__ import annotations

import re
from collections.abc import AsyncIterator, Iterator
from dataclasses import dataclass

_SEPARATOR_RE = re.compile(rb"\r\n\r\n|\n\n", re.MULTILINE)


@dataclass
class SseEvent:
    data: str
    id: str | None = None
    event: str | None = None


def _parse_block(raw: str) -> SseEvent | None:
    id_: str | None = None
    event: str | None = None
    data_lines: list[str] = []
    for line in raw.split("\n"):
        if not line:
            continue
        if line.startswith("\r"):
            line = line[1:]
        if not line:
            continue
        if line.startswith(":"):
            continue  # comment / heartbeat
        colon = line.find(":")
        if colon == -1:
            field, value = line, ""
        else:
            field = line[:colon]
            value = line[colon + 1 :]
            if value.startswith(" "):
                value = value[1:]
            if value.endswith("\r"):
                value = value[:-1]
        if field == "id":
            id_ = value
        elif field == "event":
            event = value
        elif field == "data":
            data_lines.append(value)
    if not data_lines and id_ is None and event is None:
        return None
    return SseEvent(data="\n".join(data_lines), id=id_, event=event)


def iter_sse(byte_iter: Iterator[bytes]) -> Iterator[SseEvent]:
    """Parse a synchronous bytes iterator into SSE events."""
    buffer = b""
    for chunk in byte_iter:
        if not chunk:
            continue
        buffer += chunk
        while True:
            m = _SEPARATOR_RE.search(buffer)
            if not m:
                break
            raw = buffer[: m.start()]
            buffer = buffer[m.end() :]
            ev = _parse_block(raw.decode("utf-8", errors="replace"))
            if ev is not None:
                yield ev
    if buffer:
        ev = _parse_block(buffer.decode("utf-8", errors="replace"))
        if ev is not None:
            yield ev


async def aiter_sse(byte_aiter: AsyncIterator[bytes]) -> AsyncIterator[SseEvent]:
    """Parse an async bytes iterator into SSE events."""
    buffer = b""
    async for chunk in byte_aiter:
        if not chunk:
            continue
        buffer += chunk
        while True:
            m = _SEPARATOR_RE.search(buffer)
            if not m:
                break
            raw = buffer[: m.start()]
            buffer = buffer[m.end() :]
            ev = _parse_block(raw.decode("utf-8", errors="replace"))
            if ev is not None:
                yield ev
    if buffer:
        ev = _parse_block(buffer.decode("utf-8", errors="replace"))
        if ev is not None:
            yield ev


__all__ = ["SseEvent", "aiter_sse", "iter_sse"]
