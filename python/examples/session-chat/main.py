"""Multi-turn agent session driving an interactive REPL.

Usage:
    export MANTYX_API_KEY=mtx_live_...
    export MANTYX_WORKSPACE_SLUG=acme-corp
    python main.py
"""

from __future__ import annotations

import os
import sys
from datetime import date

from pydantic import BaseModel

from mantyx import MantyxClient, define_local_tool


class TodayArgs(BaseModel):
    pass


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

    today = define_local_tool(
        name="today",
        description="Return today's date as ISO 8601 (YYYY-MM-DD).",
        parameters=TodayArgs,
        execute=lambda _args: date.today().isoformat(),
    )

    session = client.create_session(
        system_prompt=(
            "You are a friendly assistant. Use the `today` tool when the user "
            "asks anything date-related."
        ),
        tools=[today],
    )
    print(f"session id: {session.id}\n")

    print("Type a message; ^C or empty line to exit.\n")
    try:
        while True:
            try:
                prompt = input("you> ").strip()
            except EOFError:
                break
            if not prompt:
                break
            print("agent> ", end="", flush=True)
            result = session.send(
                prompt,
                on_assistant_delta=lambda d: print(d, end="", flush=True),
            )
            print()
            if not result.text:
                print("(empty reply)")
    finally:
        session.end()
        print("\nsession ended.")


if __name__ == "__main__":
    main()
