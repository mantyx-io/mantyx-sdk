"""Two flavours of Agent2Agent delegation in one ephemeral agent.

  * ``mantyx_a2a`` — public peer MANTYX dials directly (server-resolved).
  * ``define_local_a2a`` — peer the SDK reaches on MANTYX's behalf
    (client-resolved). Pass an ``agent_card_url``; the SDK fetches the
    Agent Card on the first run, ships it inline, and speaks A2A
    ``message/send`` to ``agent_card.url`` whenever MANTYX emits a
    ``local_tool_call`` for this tool. You only supply the URL.

Usage:
    export MANTYX_API_KEY=mtx_live_...
    export MANTYX_WORKSPACE_SLUG=acme-corp
    # Optional — register a real public peer:
    # export BILLING_AGENT_CARD_URL=https://billing.example.com/.well-known/agent-card.json
    # export BILLING_AGENT_TOKEN=...
    # Required — your intranet peer's Agent Card URL:
    export HR_AGENT_CARD_URL=https://hr.intranet/.well-known/agent-card.json
    # export HR_AGENT_TOKEN=...
    python main.py "How do I expense client lunches?"
"""

from __future__ import annotations

import os
import sys

from mantyx import MantyxClient, ToolRef, define_local_a2a, mantyx_a2a


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

    if os.environ.get("BILLING_AGENT_CARD_URL"):
        token = os.environ.get("BILLING_AGENT_TOKEN")
        tools.append(
            mantyx_a2a(
                name="billing_agent",
                description="Delegate billing questions to the public Acme billing agent.",
                agent_card_url=os.environ["BILLING_AGENT_CARD_URL"],
                headers={"Authorization": f"Bearer {token}"} if token else None,
            )
        )

    if os.environ.get("HR_AGENT_CARD_URL"):
        token = os.environ.get("HR_AGENT_TOKEN")
        tools.append(
            define_local_a2a(
                name="intranet_hr_agent",
                agent_card_url=os.environ["HR_AGENT_CARD_URL"],
                headers={"Authorization": f"Bearer {token}"} if token else None,
            )
        )

    if not tools:
        print(
            "Set HR_AGENT_CARD_URL (and optionally BILLING_AGENT_CARD_URL) to a reachable "
            "Agent Card endpoint.",
            file=sys.stderr,
        )
        sys.exit(1)

    prompt = (
        sys.argv[1]
        if len(sys.argv) > 1
        else "When does the company holiday calendar reset for the new fiscal year?"
    )

    result = client.run_agent(
        system_prompt=(
            "You are a helpful router. Use `billing_agent` for billing questions "
            "and `intranet_hr_agent` for HR / time-off questions. If only one "
            "delegate is available, fall back to it. Reply with the delegate's answer."
        ),
        prompt=prompt,
        tools=tools,
        reasoning_level="medium",
        on_assistant_delta=lambda d: print(d, end="", flush=True),
    )
    print()
    print("---")
    print("Final reply:", result.text)


if __name__ == "__main__":
    main()
