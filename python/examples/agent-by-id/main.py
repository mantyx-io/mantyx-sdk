"""Trigger a persisted MANTYX agent by id and merge in a local tool.

Usage:
    export MANTYX_API_KEY=mtx_live_...
    export MANTYX_WORKSPACE_SLUG=acme-corp
    export MANTYX_AGENT_ID=agent_cm6abc123
    python main.py
"""

from __future__ import annotations

import os
import sys
from pathlib import Path

from pydantic import BaseModel

from mantyx import MantyxClient, define_local_tool


class ReadFileArgs(BaseModel):
    path: str


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

    read_file = define_local_tool(
        name="read_local_file",
        description="Read a UTF-8 file from the user's machine for this run.",
        parameters=ReadFileArgs,
        execute=lambda args: Path(args.path).read_text(encoding="utf-8"),
    )

    # `system_prompt` and `model_id` are optional when `agent_id` is set —
    # the server hydrates them from the persisted agent. The local tool we
    # pass here is *merged* on top of the agent's stored tool list for this
    # single run.
    result = client.run_agent(
        agent_id=required_env("MANTYX_AGENT_ID"),
        prompt="Read /etc/hostname and tell me what it says.",
        tools=[read_file],
        on_assistant_delta=lambda d: print(d, end="", flush=True),
    )
    print()
    print("---")
    print("Final reply:", result.text)


if __name__ == "__main__":
    main()
