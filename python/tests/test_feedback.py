"""Tests for POST /agent-runs/:runId/feedback."""

from __future__ import annotations

import pytest

from mantyx import MantyxClient, MantyxError
from mantyx.async_client import AsyncMantyxClient
from tests.conftest import MockServer


def test_submit_run_feedback_creates(mantyx_client: MantyxClient, mock_server: MockServer) -> None:
    result = mantyx_client.submit_run_feedback(
        "run_abc",
        "UP",
        explanation="Nailed it",
        content_snapshot="final answer",
    )
    assert result.verdict == "UP"
    assert result.target_kind == "agent_run"
    assert result.agent_run_id == "run_abc"
    assert result.id.startswith("fb_")
    assert mock_server.last_feedback_body == {
        "verdict": "UP",
        "explanation": "Nailed it",
        "contentSnapshot": "final answer",
    }
    assert mock_server.last_feedback_created is True


def test_submit_run_feedback_updates_idempotently(
    mantyx_client: MantyxClient, mock_server: MockServer
) -> None:
    first = mantyx_client.submit_run_feedback("run_xyz", "UP")
    second = mantyx_client.submit_run_feedback("run_xyz", "DOWN", explanation="changed mind")
    assert second.id == first.id
    assert mock_server.last_feedback_created is False
    assert mock_server.last_feedback_body == {
        "verdict": "DOWN",
        "explanation": "changed mind",
    }


def test_submit_run_feedback_invalid_verdict(mantyx_client: MantyxClient) -> None:
    with pytest.raises(MantyxError, match="verdict must be"):
        mantyx_client.submit_run_feedback("run_abc", "MAYBE")  # type: ignore[arg-type]


@pytest.mark.asyncio
async def test_async_submit_run_feedback(
    async_mantyx_client: AsyncMantyxClient, mock_server: MockServer
) -> None:
    result = await async_mantyx_client.submit_run_feedback("run_async", "DOWN")
    assert result.verdict == "DOWN"
    assert result.agent_run_id == "run_async"
    assert mock_server.last_feedback_body == {"verdict": "DOWN"}
