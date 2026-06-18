"""Send file inputs with agent runs and session turns.

Demonstrates:
  - ``prompt`` + ``attachments`` shorthand on a one-shot run
  - An explicit multi-role ``messages`` array
  - ``session.send(..., attachments=[...])`` on a session turn

Usage:
    export MANTYX_API_KEY=mtx_live_...
    export MANTYX_WORKSPACE_SLUG=acme-corp
    python main.py one-shot          # default
    python main.py messages
    python main.py session
"""

from __future__ import annotations

import argparse
import base64
import os
import sys
from pathlib import Path

from mantyx import (
    ConversationMessage,
    InputFileAttachment,
    MantyxClient,
    MessageAttachment,
    input_file_attachment,
)

SAMPLE_FILE = Path(__file__).with_name("sample.txt")


def required_env(name: str) -> str:
    v = os.environ.get(name)
    if not v:
        print(f"Missing required env var {name}", file=sys.stderr)
        sys.exit(1)
    return v


def load_sample_attachment() -> InputFileAttachment:
    data = base64.b64encode(SAMPLE_FILE.read_bytes()).decode("ascii")
    return input_file_attachment(
        mime_type="text/plain",
        filename=SAMPLE_FILE.name,
        data=data,
    )


def run_one_shot(client: MantyxClient, attachment: MessageAttachment) -> None:
    print("=== one-shot: prompt + attachments ===")
    result = client.run_agent(
        system_prompt="You summarize uploaded text files in two short bullets.",
        prompt="What are the action items in the attached notes?",
        attachments=[attachment],
        on_assistant_delta=lambda d: print(d, end="", flush=True),
    )
    print()
    print("---")
    print(result.text)


def run_messages(client: MantyxClient, attachment: MessageAttachment) -> None:
    print("=== one-shot: explicit messages array ===")
    messages: list[ConversationMessage] = [
        {"role": "system", "content": "You summarize uploaded text files in two short bullets."},
        {"role": "user", "content": "Earlier: we discussed Q2 planning."},
        {"role": "assistant", "content": "Got it — send the file when you're ready."},
        {
            "role": "user",
            "content": "What's in the attached notes?",
            "attachments": [attachment],
        },
    ]
    result = client.run_agent(
        messages=messages,
        on_assistant_delta=lambda d: print(d, end="", flush=True),
    )
    print()
    print("---")
    print(result.text)


def run_session(client: MantyxClient, attachment: MessageAttachment) -> None:
    print("=== session: send with attachments ===")
    session = client.create_session(
        system_prompt="You summarize uploaded text files in two short bullets.",
    )
    try:
        result = session.send(
            "Summarize the attached planning notes.",
            attachments=[attachment],
            on_assistant_delta=lambda d: print(d, end="", flush=True),
        )
        print()
        print("---")
        print(result.text)

        # Replay frames expose attachment metadata (no bytes) on user turns.
        for event in session.events(last_messages=2):
            if event.type == "user_message":
                replay = event.user_message()
                if replay and replay.get("attachments"):
                    print("Replay metadata:", replay["attachments"])
    finally:
        session.end()


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "mode",
        nargs="?",
        default="one-shot",
        choices=("one-shot", "messages", "session"),
        help="Which API shape to demonstrate",
    )
    args = parser.parse_args()

    client = MantyxClient(
        api_key=required_env("MANTYX_API_KEY"),
        workspace_slug=required_env("MANTYX_WORKSPACE_SLUG"),
        base_url=os.environ.get("MANTYX_BASE_URL", "https://app.mantyx.io"),
    )

    attachment = load_sample_attachment()

    if args.mode == "one-shot":
        run_one_shot(client, attachment)
    elif args.mode == "messages":
        run_messages(client, attachment)
    else:
        run_session(client, attachment)


if __name__ == "__main__":
    main()
