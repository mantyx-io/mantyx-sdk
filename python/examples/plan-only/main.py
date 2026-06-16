"""Plan-only run via ``client.run_plan(...)``.

Usage:
    export MANTYX_API_KEY=mtx_live_...
    export MANTYX_WORKSPACE_SLUG=acme-corp
    python main.py
    python main.py "Roll out v2.4 to staging and prod with smoke tests in between"
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


def on_event(ev) -> None:
    if ev.type != "task_plan":
        return
    brief = ev.data.get("brief") or "(classifying…)"
    print("\n[task_plan]", brief)
    for step in ev.data.get("steps", []):
        print(f"  [{step['status']}] {step['title']}")


def main() -> None:
    client = MantyxClient(
        api_key=required_env("MANTYX_API_KEY"),
        workspace_slug=required_env("MANTYX_WORKSPACE_SLUG"),
        base_url=os.environ.get("MANTYX_BASE_URL", "https://app.mantyx.io"),
    )

    prompt = " ".join(sys.argv[1:]).strip() or (
        "Migrate billing tables from Postgres 14 to 16 and backfill historical rows."
    )

    result = client.run_plan(
        system_prompt="You break complex engineering work into ordered, actionable steps.",
        prompt=prompt,
        # Uncomment to skip the classifier:
        # brief="Postgres billing migration",
        # steps=["Snapshot schema", "Apply DDL", "Backfill rows", "Verify counts"],
        on_event=on_event,
    )

    print("\n---\nSummary:\n", result.text)
    if not result.plan or not result.plan.get("steps"):
        print("\n(No multi-step plan — classifier declined.)")
        return

    print("\nStructured plan:")
    if brief := result.plan.get("brief"):
        print("Brief:", brief)
    for step in result.plan.get("steps", []):
        print(f"  [{step['status']}] {step['title']}")


if __name__ == "__main__":
    main()
