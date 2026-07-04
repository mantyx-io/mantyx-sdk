"""Tests for eval datasets and runs."""

from __future__ import annotations

import pytest

from mantyx import InlineEvalCaseSpec, InlineEvalDatasetSpec, MantyxClient, MantyxError
from mantyx.async_client import AsyncMantyxClient
from tests.conftest import MockServer


def test_list_eval_datasets(mantyx_client: MantyxClient) -> None:
    out = mantyx_client.list_eval_datasets()
    assert len(out.datasets) == 1
    assert out.datasets[0].id == "ds_demo"


def test_get_eval_dataset(mantyx_client: MantyxClient) -> None:
    detail = mantyx_client.get_eval_dataset("ds_demo")
    assert detail.id == "ds_demo"
    assert len(detail.cases) == 1
    assert detail.cases[0].name == "hello"


def test_create_and_get_eval_run(mantyx_client: MantyxClient, mock_server: MockServer) -> None:
    accepted = mantyx_client.create_eval_run(
        dataset_id="ds_demo",
        agent_id="agent_1",
    )
    assert accepted.run_id.startswith("eval_")
    assert accepted.stream_url.endswith("/stream")

    detail = mantyx_client.get_eval_run(accepted.run_id)
    assert detail.status == "succeeded"
    assert detail.passed_cases == 1
    assert mock_server.last_eval_create_body == {
        "datasetId": "ds_demo",
        "agentId": "agent_1",
    }


def test_create_eval_run_inline_dataset(
    mantyx_client: MantyxClient, mock_server: MockServer
) -> None:
    dataset = InlineEvalDatasetSpec(
        name="inline",
        cases=[InlineEvalCaseSpec(input={"role": "user", "content": "ping"})],
    )
    accepted = mantyx_client.create_eval_run(
        dataset=dataset,
        agent={"systemPrompt": "You are helpful.", "tools": []},
    )
    assert accepted.run_id
    body = mock_server.last_eval_create_body or {}
    assert body["dataset"]["name"] == "inline"
    assert body["agent"]["systemPrompt"] == "You are helpful."


def test_create_eval_run_validation(mantyx_client: MantyxClient) -> None:
    with pytest.raises(MantyxError, match="dataset_id or dataset"):
        mantyx_client.create_eval_run(agent_id="a1")


def test_list_and_compare_eval_runs(mantyx_client: MantyxClient) -> None:
    a = mantyx_client.create_eval_run(dataset_id="ds_demo", agent_id="agent_1")
    b = mantyx_client.create_eval_run(dataset_id="ds_demo", agent_id="agent_1", model_id="m1")
    listed = mantyx_client.list_eval_runs()
    assert len(listed.runs) >= 2
    compared = mantyx_client.compare_eval_runs(a.run_id, b.run_id)
    assert compared.run_a.id == a.run_id
    assert compared.run_b.id == b.run_id


def test_stream_eval_run(mantyx_client: MantyxClient) -> None:
    accepted = mantyx_client.create_eval_run(dataset_id="ds_demo", agent_id="agent_1")
    events = list(mantyx_client.stream_eval_run(accepted.run_id))
    assert any(e.type == "snapshot" for e in events)
    assert events[-1].type == "run_completed"


def test_run_eval_blocks_until_terminal(mantyx_client: MantyxClient) -> None:
    detail = mantyx_client.run_eval(
        dataset_id="ds_demo",
        agent_id="agent_1",
        poll_interval_s=0.01,
    )
    assert detail.status == "succeeded"


def test_cancel_eval_run(mantyx_client: MantyxClient) -> None:
    accepted = mantyx_client.create_eval_run(dataset_id="ds_demo", agent_id="agent_1")
    out = mantyx_client.cancel_eval_run(accepted.run_id)
    assert out["ok"] is True
    detail = mantyx_client.get_eval_run(accepted.run_id)
    assert detail.status == "cancelled"


@pytest.mark.asyncio
async def test_async_eval_run(async_mantyx_client: AsyncMantyxClient) -> None:
    accepted = await async_mantyx_client.create_eval_run(
        dataset_id="ds_demo",
        agent_id="agent_1",
    )
    detail = await async_mantyx_client.get_eval_run(accepted.run_id)
    assert detail.status == "succeeded"
