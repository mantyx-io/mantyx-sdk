"""Public API for the MANTYX Python SDK.

Example:
    >>> from pydantic import BaseModel
    >>> from mantyx import MantyxClient, define_local_tool
    >>>
    >>> class ReadFileArgs(BaseModel):
    ...     path: str
    >>>
    >>> client = MantyxClient(api_key="...", workspace_slug="acme-corp")
    >>> result = client.run_agent(
    ...     system_prompt="You are a helpful filesystem assistant.",
    ...     prompt="Read /etc/hostname and tell me what it says.",
    ...     tools=[
    ...         define_local_tool(
    ...             name="read_file",
    ...             parameters=ReadFileArgs,
    ...             execute=lambda args: open(args.path).read(),
    ...         ),
    ...     ],
    ... )
    >>> print(result.text)
"""

from __future__ import annotations

from ._version import SDK_VERSION, __version__
from .async_client import AsyncAgentSession, AsyncMantyxClient
from .client import (
    DEFAULT_BASE_URL,
    AgentSession,
    MantyxClient,
    ModelCatalog,
    ModelInfo,
    PricingInfo,
    RunEvent,
    RunResult,
    SessionInfo,
)
from .errors import (
    MantyxAuthError,
    MantyxError,
    MantyxNetworkError,
    MantyxRunError,
    MantyxToolError,
)
from .tools import (
    LocalTool,
    MantyxPluginToolRef,
    MantyxToolRef,
    ToolRef,
    define_local_tool,
    is_local_tool,
    mantyx_plugin_tool,
    mantyx_tool,
)

__all__ = [
    "DEFAULT_BASE_URL",
    "SDK_VERSION",
    "AgentSession",
    "AsyncAgentSession",
    "AsyncMantyxClient",
    "LocalTool",
    "MantyxAuthError",
    "MantyxClient",
    "MantyxError",
    "MantyxNetworkError",
    "MantyxPluginToolRef",
    "MantyxRunError",
    "MantyxToolError",
    "MantyxToolRef",
    "ModelCatalog",
    "ModelInfo",
    "PricingInfo",
    "RunEvent",
    "RunResult",
    "SessionInfo",
    "ToolRef",
    "__version__",
    "define_local_tool",
    "is_local_tool",
    "mantyx_plugin_tool",
    "mantyx_tool",
]
