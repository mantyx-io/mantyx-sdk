"""Print every event from an agent run as it arrives.

Usage:
    export MANTYX_API_KEY=mtx_live_...
    export MANTYX_WORKSPACE_SLUG=acme-corp
    python main.py
"""

from __future__ import annotations

import json
import os
import sys

from mantyx import MantyxClient


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

    print("=== streaming events ===")
    for event in client.stream_agent(
        system_prompt="You are a friendly storyteller.",
        prompt="Tell me a short story (2-3 sentences) about a curious robot.",
    ):
        if event.type == "assistant_delta":
            print(event.text, end="", flush=True)
        elif event.type == "result":
            print()
            print("--- terminal event ---")
            print(json.dumps(event.data, indent=2))
        else:
            print(f"\n[{event.type}] {json.dumps(event.data, default=str)}")


if __name__ == "__main__":
    main()
