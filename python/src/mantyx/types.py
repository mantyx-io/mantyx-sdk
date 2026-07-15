"""Wire-protocol TypedDicts for the MANTYX Python SDK.

These types mirror the JSON shapes documented in ``docs/agent-runs-protocol.md``
and ``docs/wire-protocol.md``. They are structural: plain ``dict`` literals
passed at call sites are accepted by type checkers when they match the shape.
"""

from __future__ import annotations

import sys
from typing import Any, Literal, TypedDict

if sys.version_info >= (3, 11):
    from typing import Required
else:
    from typing_extensions import Required

from ._schema import JsonSchema

# ------------------------------------------------------------------ Metadata

#: Flat string→string KV on runs and sessions (max 16 entries server-side).
Metadata = dict[str, str]

# ---------------------------------------------------------------- Conversation

MessageRole = Literal["user", "assistant", "system"]
SessionStatus = Literal["active", "ended"]


class InputFileAttachment(TypedDict):
    """Inline file bytes on a user message (base64, no data-URL prefix)."""

    type: Literal["input_file"]
    mimeType: str
    filename: str
    data: str


class InputFileUrlAttachment(TypedDict, total=False):
    """HTTPS URL the provider fetches as a user-message file input."""

    type: Required[Literal["input_file_url"]]
    url: Required[str]
    mimeType: str
    filename: str


MessageAttachment = InputFileAttachment | InputFileUrlAttachment


class InputFileAttachmentMetadata(TypedDict):
    """Replay metadata for an inline file on ``user_message`` frames (no bytes)."""

    type: Literal["input_file"]
    mimeType: str
    filename: str
    size: int


class InputFileUrlAttachmentMetadata(TypedDict, total=False):
    """Replay metadata for a URL file on ``user_message`` frames."""

    type: Required[Literal["input_file_url"]]
    url: Required[str]
    mimeType: str
    filename: str


AttachmentMetadata = InputFileAttachmentMetadata | InputFileUrlAttachmentMetadata


class ConversationMessage(TypedDict, total=False):
    """One entry in a multi-role ``messages`` array."""

    role: MessageRole
    content: str
    # On requests: file inputs to send. On session history / replay: metadata only.
    attachments: list[MessageAttachment | AttachmentMetadata]


def input_file_attachment(
    *,
    mime_type: str,
    filename: str,
    data: str,
) -> InputFileAttachment:
    """Build an inline file attachment for the last user message in a run."""
    return {
        "type": "input_file",
        "mimeType": mime_type,
        "filename": filename,
        "data": data,
    }


def input_file_url_attachment(
    *,
    url: str,
    mime_type: str | None = None,
    filename: str | None = None,
) -> InputFileUrlAttachment:
    """Build a URL file attachment for the last user message in a run."""
    out: InputFileUrlAttachment = {"type": "input_file_url", "url": url}
    if mime_type is not None:
        out["mimeType"] = mime_type
    if filename is not None:
        out["filename"] = filename
    return out


# ------------------------------------------------------------- Agent / run spec


class RunBudgets(TypedDict, total=False):
    """Optional per-run turn caps on the agent spec."""

    maxToolTurns: int


class AgentSpec(TypedDict, total=False):
    """Agent configuration stored on a session row (``GET /agent-sessions/:id``)."""

    systemPrompt: str
    agentId: str
    modelId: str
    name: str
    tools: list[dict[str, Any]]
    reasoningLevel: str | int
    loopDetection: bool | dict[str, Any]
    toolBudgets: dict[str, dict[str, int]]
    supervisor: bool | dict[str, Any]
    plan: bool | str | dict[str, Any]
    outputSchema: dict[str, Any]
    metadata: Metadata
    budgets: RunBudgets


# --------------------------------------------------------------- Run events

#: Raw event payload — specific shapes vary by ``RunEvent.type``.
RunEventData = dict[str, Any]


class UserMessageEventData(TypedDict, total=False):
    """Payload on session-replay ``user_message`` frames."""

    text: str
    attachments: list[AttachmentMetadata]


class AssistantMessageEventData(TypedDict, total=False):
    """Payload on ``assistant_message`` frames."""

    text: str
    toolCalls: list[dict[str, Any]]


class RunTokenUsageWire(TypedDict, total=False):
    """Token totals on terminal ``result`` / ``error`` events."""

    inputTokens: int
    cachedTokens: int
    reasoningTokens: int
    outputTokens: int


class RunModelInfoWire(TypedDict, total=False):
    """Resolved model on terminal events (MANTYX ≥ 2026-09)."""

    id: str
    provider: str
    vendorModelId: str
    reasoningEffort: str


class ResultEventData(TypedDict, total=False):
    """Payload on successful terminal ``result`` events."""

    subtype: str
    text: str
    tokens: RunTokenUsageWire
    turns: int
    model: RunModelInfoWire
    plan: dict[str, Any]


class ErrorEventData(TypedDict, total=False):
    """Payload on terminal ``error`` events."""

    error: str
    code: str
    errorClass: str
    finishReason: str
    partialText: str
    tokens: RunTokenUsageWire
    turns: int
    model: RunModelInfoWire


__all__ = [
    "AgentSpec",
    "AssistantMessageEventData",
    "AttachmentMetadata",
    "ConversationMessage",
    "ErrorEventData",
    "InputFileAttachment",
    "InputFileAttachmentMetadata",
    "InputFileUrlAttachment",
    "InputFileUrlAttachmentMetadata",
    "JsonSchema",
    "MessageAttachment",
    "MessageRole",
    "Metadata",
    "ResultEventData",
    "RunBudgets",
    "RunEventData",
    "RunModelInfoWire",
    "RunTokenUsageWire",
    "SessionStatus",
    "UserMessageEventData",
    "input_file_attachment",
    "input_file_url_attachment",
]
