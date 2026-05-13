"""Public API for the MANTYX Python SDK.

Example:
    >>> from pydantic import BaseModel
    >>> from mantyx import MantyxClient, define_local_tool
    >>>
    >>> class ReadFileArgs(BaseModel):
    ...     path: str
    >>>
    >>> # Workspace API key (token prefix ``mantyx_``):
    >>> client = MantyxClient(api_key="...", workspace_slug="acme-corp")
    >>>
    >>> # Or, equivalently, a MANTYX OAuth 2.0 access token
    >>> # (token prefix ``mantyx_at_``):
    >>> client = MantyxClient(access_token="...", workspace_slug="acme-corp")
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
    parse_run_output,
)
from .errors import (
    MantyxAuthError,
    MantyxError,
    MantyxNetworkError,
    MantyxOAuthError,
    MantyxParseError,
    MantyxRunError,
    MantyxScopeError,
    MantyxToolError,
)
from .oauth import (
    DEFAULT_OAUTH_BASE_URL,
    DEFAULT_REFRESH_SKEW_S,
    AsyncTokenSource,
    MantyxOAuthClient,
    OAuthToken,
    TokenRequestReason,
    TokenSource,
)
from .tools import (
    LocalA2ATool,
    LocalMcpHttpTransport,
    LocalMcpServer,
    LocalMcpStdioTransport,
    LocalTool,
    LoopDetection,
    MantyxA2AToolRef,
    MantyxMcpToolRef,
    MantyxPluginToolRef,
    MantyxToolRef,
    OutputSchema,
    ReasoningLevel,
    ToolBudget,
    ToolBudgets,
    ToolRef,
    define_local_a2a,
    define_local_mcp,
    define_local_tool,
    is_local_a2a_tool,
    is_local_mcp_server,
    is_local_tool,
    mantyx_a2a,
    mantyx_mcp,
    mantyx_plugin_tool,
    mantyx_tool,
)

__all__ = [
    "DEFAULT_BASE_URL",
    "DEFAULT_OAUTH_BASE_URL",
    "DEFAULT_REFRESH_SKEW_S",
    "SDK_VERSION",
    "AgentSession",
    "AsyncAgentSession",
    "AsyncMantyxClient",
    "AsyncTokenSource",
    "LocalA2ATool",
    "LocalMcpHttpTransport",
    "LocalMcpServer",
    "LocalMcpStdioTransport",
    "LocalTool",
    "LoopDetection",
    "MantyxA2AToolRef",
    "MantyxAuthError",
    "MantyxClient",
    "MantyxError",
    "MantyxMcpToolRef",
    "MantyxNetworkError",
    "MantyxOAuthClient",
    "MantyxOAuthError",
    "MantyxParseError",
    "MantyxPluginToolRef",
    "MantyxRunError",
    "MantyxScopeError",
    "MantyxToolError",
    "MantyxToolRef",
    "ModelCatalog",
    "ModelInfo",
    "OAuthToken",
    "OutputSchema",
    "PricingInfo",
    "ReasoningLevel",
    "RunEvent",
    "RunResult",
    "SessionInfo",
    "TokenRequestReason",
    "TokenSource",
    "ToolBudget",
    "ToolBudgets",
    "ToolRef",
    "__version__",
    "define_local_a2a",
    "define_local_mcp",
    "define_local_tool",
    "is_local_a2a_tool",
    "is_local_mcp_server",
    "is_local_tool",
    "mantyx_a2a",
    "mantyx_mcp",
    "mantyx_plugin_tool",
    "mantyx_tool",
    "parse_run_output",
]
