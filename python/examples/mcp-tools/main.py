"""Two flavours of MCP connector in one ephemeral agent.

  * ``mantyx_mcp`` — remote MCP server (Streamable HTTP) MANTYX dials,
    lists the catalog of, and proxies. MANTYX prefixes every discovered
    tool name as ``<server>_<tool>``.
  * ``define_local_mcp`` — MCP server fully managed by the SDK. Pass
    either a Streamable HTTP ``url`` or an stdio ``command``; the SDK
    opens the transport, runs ``Initialize`` + ``tools/list`` on the
    first run, forwards every ``local_tool_call`` into ``tools/call``,
    and closes the transport when the run / session ends. The model-facing
    names are ``<server>_<tool>`` regardless of which side resolves the
    call. The SDK uses the official ``mcp`` Python package under the hood.

Usage:
    export MANTYX_API_KEY=mtx_live_...
    export MANTYX_WORKSPACE_SLUG=acme-corp
    # Optional — register a real public MCP server:
    # export GH_MCP_URL=https://api.example.com/mcp
    # export GH_PAT=...
    # export GH_TOOL_FILTER=create_issue,list_issues
    # And one of:
    #   export FS_MCP_URL=http://localhost:8080/mcp     # Streamable HTTP
    #   export FS_MCP_COMMAND="mcp-server-filesystem ." # stdio (space-split)
    python main.py "List the first 10 entries of the current directory."
"""

from __future__ import annotations

import os
import shlex
import sys

from mantyx import MantyxClient, ToolRef, define_local_mcp, mantyx_mcp


def required_env(name: str) -> str:
    v = os.environ.get(name)
    if not v:
        print(f"Missing required env var {name}", file=sys.stderr)
        sys.exit(1)
    return v


def main() -> None:
    client = MantyxClient(
        api_key=required_env("MANTYX_API_KEY"),
        workspace_slug=required_env("MANTYX_WORKSPACE_SLUG"),
        base_url=os.environ.get("MANTYX_BASE_URL", "https://app.mantyx.io"),
    )

    tools: list[ToolRef] = []

    # Local MCP — pick whichever transport your server speaks.
    if os.environ.get("FS_MCP_URL"):
        token = os.environ.get("FS_MCP_TOKEN")
        tools.append(
            define_local_mcp(
                name="fs",
                url=os.environ["FS_MCP_URL"],
                headers={"Authorization": f"Bearer {token}"} if token else None,
            )
        )
    elif os.environ.get("FS_MCP_COMMAND"):
        parts = shlex.split(os.environ["FS_MCP_COMMAND"])
        if not parts:
            print("FS_MCP_COMMAND must be a non-empty command string", file=sys.stderr)
            sys.exit(1)
        cmd, *cmd_args = parts
        tools.append(
            define_local_mcp(
                name="fs",
                command=cmd,
                args=cmd_args or None,
            )
        )

    if os.environ.get("GH_MCP_URL"):
        pat = os.environ.get("GH_PAT")
        tool_filter = None
        if os.environ.get("GH_TOOL_FILTER"):
            tool_filter = [s.strip() for s in os.environ["GH_TOOL_FILTER"].split(",") if s.strip()]
        tools.append(
            mantyx_mcp(
                name="github",
                url=os.environ["GH_MCP_URL"],
                headers={"Authorization": f"Bearer {pat}"} if pat else None,
                tool_filter=tool_filter,
            )
        )

    if not tools:
        print(
            "Set FS_MCP_URL (Streamable HTTP) or FS_MCP_COMMAND (stdio) — and optionally "
            "GH_MCP_URL.",
            file=sys.stderr,
        )
        sys.exit(1)

    prompt = (
        sys.argv[1]
        if len(sys.argv) > 1
        else "List the first 10 entries of the current working directory."
    )

    result = client.run_agent(
        system_prompt=(
            "You are a developer assistant. "
            "Use `fs_*` tools for the local filesystem and `github_*` tools "
            "for repository questions. Reply with a short summary."
        ),
        prompt=prompt,
        tools=tools,
        reasoning_level="low",
        on_assistant_delta=lambda d: print(d, end="", flush=True),
    )
    print()
    print("---")
    print("Final reply:", result.text)


if __name__ == "__main__":
    main()
