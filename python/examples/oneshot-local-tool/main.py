"""One-shot agent run with a single local tool.

Usage:
    export MANTYX_API_KEY=mtx_live_...
    export MANTYX_WORKSPACE_SLUG=acme-corp
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


def read_file(args: ReadFileArgs) -> str:
    return Path(args.path).read_text(encoding="utf-8")


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

    tool = define_local_tool(
        name="read_file",
        description="Read a UTF-8 file from the local filesystem.",
        parameters=ReadFileArgs,
        execute=read_file,
    )

    result = client.run_agent(
        system_prompt="You are a code review assistant. Use read_file when asked.",
        prompt="Read /etc/hostname and tell me what it says in one sentence.",
        tools=[tool],
        on_assistant_delta=lambda d: print(d, end="", flush=True),
    )
    print()
    print("---")
    print("Final reply:", result.text)


if __name__ == "__main__":
    main()
