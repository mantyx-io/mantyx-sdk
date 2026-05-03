"""Public tool helpers for the MANTYX SDK.

Three tool kinds, all carried inside the agent spec:

  * ``mantyx`` — server-side workspace ``Tool`` row referenced by id.
  * ``mantyx_plugin`` — a platform plugin tool referenced by name.
  * ``local`` — defined and executed in the SDK's process. The MANTYX
    server pauses the agent loop, emits a ``local_tool_call`` event,
    and waits for the SDK to POST a tool-result back.
"""

from __future__ import annotations

import asyncio
import inspect
import re
from collections.abc import Awaitable
from dataclasses import dataclass, field
from typing import Any, Callable, Union

from ._schema import ParametersInput

ToolName = str

_LOCAL_TOOL_NAME_RE = re.compile(r"^[a-zA-Z0-9_]{1,64}$")
_PLUGIN_TOOL_NAME_RE = re.compile(r"^@[a-z][a-z0-9_-]*/[a-z][a-z0-9_-]*$")


@dataclass(frozen=True)
class LocalTool:
    """A tool whose handler runs inside this Python process.

    Build with :func:`define_local_tool`.
    """

    name: ToolName
    description: str = ""
    parameters: ParametersInput = None
    execute: Callable[..., Any] = field(default=lambda *_: "")
    kind: str = "local"


@dataclass(frozen=True)
class MantyxToolRef:
    """Reference to a workspace ``Tool`` row resolved server-side by id."""

    id: str
    kind: str = "mantyx"


@dataclass(frozen=True)
class MantyxPluginToolRef:
    """Reference to a platform plugin tool resolved server-side by name."""

    name: str
    kind: str = "mantyx_plugin"


ToolRef = Union[LocalTool, MantyxToolRef, MantyxPluginToolRef]


def define_local_tool(
    *,
    name: ToolName,
    execute: Callable[..., Any],
    description: str = "",
    parameters: ParametersInput = None,
) -> LocalTool:
    """Define a tool whose handler runs in *this* Python process.

    Args:
        name: ``[a-zA-Z0-9_]{1,64}`` — what the model addresses the tool by.
        execute: Sync or async callable. Receives parsed args (a Pydantic
            model instance if ``parameters`` is a model, otherwise a
            ``dict``). Must return a string. Non-string returns are
            JSON-serialised by the SDK before being POSTed back.
        description: Free-form description for the model.
        parameters: A Pydantic v2 ``BaseModel`` subclass, a JSON Schema
            dict, or ``None`` for "any object".
    """
    if not _LOCAL_TOOL_NAME_RE.match(name):
        raise ValueError(f"Invalid local tool name {name!r}: must match /^[a-zA-Z0-9_]{{1,64}}$/")
    return LocalTool(
        name=name,
        description=description or "",
        parameters=parameters,
        execute=execute,
    )


def mantyx_tool(tool_id: str) -> MantyxToolRef:
    """Reference a workspace ``Tool`` by its ``tool_<cuid>`` id."""
    if not isinstance(tool_id, str) or not tool_id:
        raise ValueError("mantyx_tool(id): id must be a non-empty string")
    return MantyxToolRef(id=tool_id)


def mantyx_plugin_tool(name: str) -> MantyxPluginToolRef:
    """Reference an installed platform plugin tool by ``@plugin-slug/tool-name``."""
    if not isinstance(name, str) or not _PLUGIN_TOOL_NAME_RE.match(name):
        raise ValueError(
            f"mantyx_plugin_tool(name): expected '@plugin-slug/tool-name', got {name!r}"
        )
    return MantyxPluginToolRef(name=name)


def is_local_tool(t: ToolRef) -> bool:
    return isinstance(t, LocalTool)


async def maybe_await(value: Awaitable[Any] | Any) -> Any:
    """Helper: awaits a coroutine, otherwise returns the value as-is."""
    if inspect.isawaitable(value):
        return await value
    return value


def call_handler_sync(handler: Callable[..., Any], parsed_args: Any) -> Any:
    """Invoke ``handler(parsed_args)``; if it returns a coroutine, run it
    on a fresh event loop. Used by the synchronous client."""
    out = handler(parsed_args)
    if inspect.isawaitable(out):
        return asyncio.run(maybe_await(out))
    return out


__all__ = [
    "LocalTool",
    "MantyxPluginToolRef",
    "MantyxToolRef",
    "ToolName",
    "ToolRef",
    "call_handler_sync",
    "define_local_tool",
    "is_local_tool",
    "mantyx_plugin_tool",
    "mantyx_tool",
    "maybe_await",
]


def collect_local_handlers(
    tools: list[ToolRef] | None,
) -> dict[ToolName, LocalTool]:
    """Build the ``name → LocalTool`` lookup the run loop dispatches against."""
    out: dict[ToolName, LocalTool] = {}
    if not tools:
        return out
    for t in tools:
        if isinstance(t, LocalTool):
            out[t.name] = t
    return out


def serialize_tool_refs(tools: list[ToolRef] | None) -> list[dict[str, Any]]:
    """Translate the in-process ``ToolRef`` list into the wire dict shape."""
    from ._schema import to_tool_parameters_wire

    if not tools:
        return []
    out: list[dict[str, Any]] = []
    for t in tools:
        if isinstance(t, MantyxToolRef):
            out.append({"kind": "mantyx", "id": t.id})
        elif isinstance(t, MantyxPluginToolRef):
            out.append({"kind": "mantyx_plugin", "name": t.name})
        elif isinstance(t, LocalTool):
            out.append(
                {
                    "kind": "local",
                    "name": t.name,
                    "description": t.description,
                    "parameters": to_tool_parameters_wire(t.parameters),
                }
            )
        else:  # pragma: no cover — defensive
            raise TypeError(f"Unknown ToolRef kind: {type(t).__name__}")
    return out
