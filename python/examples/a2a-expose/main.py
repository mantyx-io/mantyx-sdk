"""Expose a MANTYX agent over the Agent2Agent (A2A) protocol.

Spins up an A2A server backed by a MANTYX agent (ephemeral or persisted).
Other agents can discover it at:
    GET http://localhost:4000/.well-known/agent-card.json

…and call it over JSON-RPC at the root path. Each unique A2A `contextId`
maps to a long-lived MANTYX session, so multi-turn conversations share
history without any extra plumbing.

Usage:
    pip install "mantyx-sdk[a2a-server]"
    export MANTYX_API_KEY=mtx_live_...
    export MANTYX_WORKSPACE_SLUG=acme-corp

    # Either point at a persisted MANTYX agent…
    export MANTYX_AGENT_ID=agent_cm6abc123

    # …or rely on the default ephemeral system prompt.
    # export SYSTEM_PROMPT="You are a billing assistant."

    python main.py
"""

from __future__ import annotations

import asyncio
import os
import signal

from mantyx import AsyncMantyxClient
from mantyx.a2a_server import build_agent_card, serve_agent_over_a2a


def required_env(name: str) -> str:
    v = os.environ.get(name)
    if not v:
        print(f"Missing required env var: {name}")
        raise SystemExit(1)
    return v


async def main() -> None:
    api_key = required_env("MANTYX_API_KEY")
    workspace_slug = required_env("MANTYX_WORKSPACE_SLUG")

    port = int(os.environ.get("PORT", "4000"))
    public_url = os.environ.get("PUBLIC_URL", f"http://localhost:{port}")

    card = build_agent_card(
        name=os.environ.get("AGENT_NAME", "MANTYX Demo Agent"),
        description=os.environ.get(
            "AGENT_DESCRIPTION",
            "A MANTYX agent exposed as an Agent2Agent peer.",
        ),
        version="1.0.0",
        public_url=public_url,
    )

    async with AsyncMantyxClient(api_key=api_key, workspace_slug=workspace_slug) as client:
        agent_id = os.environ.get("MANTYX_AGENT_ID")
        if agent_id:
            handle = await serve_agent_over_a2a(
                client=client,
                agent_card=card,
                agent_id=agent_id,
                port=port,
            )
        else:
            handle = await serve_agent_over_a2a(
                client=client,
                agent_card=card,
                system_prompt=os.environ.get(
                    "SYSTEM_PROMPT",
                    "You are a friendly MANTYX assistant. Keep replies concise.",
                ),
                model_id=os.environ.get("MODEL_ID"),
                port=port,
            )

        print(f"MANTYX agent live at {handle.url}")
        print(f"Agent Card:    {handle.url}/.well-known/agent-card.json")
        print(f"JSON-RPC:      {handle.url}/")
        print(f"HTTP+JSON:     {handle.url}/v1")

        loop = asyncio.get_running_loop()
        stop = loop.create_future()

        def _signal_handler() -> None:
            if not stop.done():
                stop.set_result(None)

        for sig in (signal.SIGINT, signal.SIGTERM):
            try:
                loop.add_signal_handler(sig, _signal_handler)
            except NotImplementedError:  # Windows
                pass

        try:
            await stop
        finally:
            print("\nShutting down…")
            await handle.aclose()


if __name__ == "__main__":
    asyncio.run(main())
