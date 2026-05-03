"""Mix MANTYX workspace tools, plugin tools, and a local tool in one run.

Usage:
    export MANTYX_API_KEY=mtx_live_...
    export MANTYX_WORKSPACE_SLUG=acme-corp
    export MANTYX_TOOL_ID=tool_cm6abc123        # a workspace `Tool` row
    export MANTYX_PLUGIN_TOOL=@web/search       # an installed plugin tool
    python main.py
"""

from __future__ import annotations

import json
import os
import sys

from pydantic import BaseModel

from mantyx import (
    MantyxClient,
    define_local_tool,
    mantyx_plugin_tool,
    mantyx_tool,
)


class SaveNoteArgs(BaseModel):
    title: str
    body: str


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
        base_url=os.environ.get("MANTYX_BASE_URL", "https://api.mantyx.com"),
    )

    save_note = define_local_tool(
        name="save_note",
        description="Save a research note locally and return its filename.",
        parameters=SaveNoteArgs,
        execute=lambda args: json.dumps(
            {"saved": True, "path": f"/tmp/{args.title.replace(' ', '_')}.md"}
        ),
    )

    tools = [save_note]
    if os.environ.get("MANTYX_TOOL_ID"):
        tools.append(mantyx_tool(os.environ["MANTYX_TOOL_ID"]))
    if os.environ.get("MANTYX_PLUGIN_TOOL"):
        tools.append(mantyx_plugin_tool(os.environ["MANTYX_PLUGIN_TOOL"]))

    result = client.run_agent(
        system_prompt=(
            "You are a research assistant. Look things up with the available "
            "search tools, and save a brief summary using save_note."
        ),
        prompt="Look up the latest CPI release and save a one-paragraph summary.",
        tools=tools,
        on_assistant_delta=lambda d: print(d, end="", flush=True),
    )
    print()
    print("---")
    print("Final reply:", result.text)


if __name__ == "__main__":
    main()
