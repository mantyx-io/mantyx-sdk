"""List the workspace's model catalog and run an agent on the default model.

Usage:
    export MANTYX_API_KEY=mtx_live_...
    export MANTYX_WORKSPACE_SLUG=acme-corp
    python main.py
"""

from __future__ import annotations

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

    catalog = client.list_models()
    print(f"default: {catalog.default_model_id}")
    print()
    print(f"{'id':<36}  {'provider':<12}  {'vendor':<24}  {'context':<8}  label")
    print("-" * 100)
    for m in catalog.models:
        ctx = str(m.context_window_tokens) if m.context_window_tokens is not None else "-"
        print(f"{m.id:<36}  {m.provider:<12}  {m.vendor_model_id:<24}  {ctx:<8}  {m.label}")

    if not catalog.default_model_id:
        print("\n(no default model configured)")
        return

    print(f"\nrunning a smoke test against {catalog.default_model_id}...\n")
    result = client.run_agent(
        model_id=catalog.default_model_id,
        system_prompt="You are friendly.",
        prompt="In one sentence, who are you?",
    )
    print(result.text)


if __name__ == "__main__":
    main()
