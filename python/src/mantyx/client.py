"""Synchronous MANTYX client: HTTP plumbing, model catalog, run + session drivers.

The wire protocol is identical to the TypeScript and Go SDKs and is
specified in ``docs/agent-runs-protocol.md`` (a copy ships with this
package under ``docs/``).
"""

from __future__ import annotations

import json
import threading
import time
from collections.abc import Callable, Iterable, Iterator, Mapping, Sequence
from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass, field
from typing import (
    Any,
    Literal,
    cast,
)

import httpx

from ._local_resolver import (
    _SyncMcpPortal,
    sync_call_a2a,
    sync_close_mcp_refs,
    sync_resolve_local_refs,
)
from ._schema import parse_args_with_pydantic
from ._version import SDK_VERSION
from .errors import (
    MantyxAuthError,
    MantyxError,
    MantyxNetworkError,
    MantyxParseError,
    MantyxRunError,
    MantyxScopeError,
    MantyxToolError,
)
from .oauth import TokenSource
from .sse import SseEvent, iter_sse
from .tools import (
    LoopDetection,
    OutputSchema,
    PlanSpec,
    ReasoningLevel,
    Supervisor,
    TaskPlan,
    ToolBudgets,
    ToolRef,
    _LocalHandlers,
    collect_local_handlers,
    normalize_loop_detection,
    normalize_output_schema,
    normalize_plan,
    normalize_reasoning_level,
    normalize_supervisor,
    normalize_tool_budgets,
    parse_task_plan,
    plan_only,
    serialize_tool_refs,
)
from .types import (
    AgentSpec,
    AttachmentMetadata,
    ConversationMessage,
    MessageAttachment,
    Metadata,
    RunBudgets,
    RunEventData,
    UserMessageEventData,
)

DEFAULT_BASE_URL = "https://app.mantyx.io"
DEFAULT_TIMEOUT_S = 60.0

# Sentinel value for "argument not provided". Distinct from ``None`` because
# ``None``/``False`` are valid wire values for ``loop_detection`` (``False``
# disables the guard) — the helpers need to tell "omit" from "set to that".
_UNSET: Any = object()


@dataclass
class PricingInfo:
    inputPer1MUsd: float | None = None
    outputPer1MUsd: float | None = None
    cacheReadPer1MUsd: float | None = None


@dataclass
class ModelInfo:
    id: str
    label: str
    provider: str
    vendor_model_id: str
    source: str  # "workspace_provider" | "platform_offering"
    context_window_tokens: int | None
    pricing: PricingInfo | None


@dataclass
class ModelCatalog:
    models: list[ModelInfo]
    default_model_id: str | None


@dataclass
class RunEvent:
    """One durable run event. Specific payload fields vary by ``type``."""

    seq: int
    type: str
    data: RunEventData = field(default_factory=dict)

    @property
    def text(self) -> str:
        """Convenience for `assistant_delta` / `assistant_message` events."""
        v = self.data.get("text")
        return v if isinstance(v, str) else ""

    def user_message(self) -> UserMessageEventData | None:
        """Typed view of ``user_message`` replay frames, if applicable."""
        if self.type != "user_message":
            return None
        return cast(UserMessageEventData, self.data)


@dataclass
class RunTokenUsage:
    """Per-run token totals carried on terminal ``result`` / ``error``
    events (and the ``GET /agent-runs/:runId`` snapshot) by MANTYX
    ≥ 2026-09. See ``docs/agent-runs-protocol.md`` §7.1 for the
    per-provider mapping.

    ``input_tokens`` / ``output_tokens`` are the billable totals;
    ``cached_tokens`` and ``reasoning_tokens`` are diagnostic
    breakdowns *inside* those two totals (not separate additive
    buckets). All four are clamped to non-negative integers so a
    misbehaving engine can never poison downstream dashboards.
    """

    input_tokens: int = 0
    cached_tokens: int = 0
    reasoning_tokens: int = 0
    output_tokens: int = 0


@dataclass
class RunModelInfo:
    """The resolved model that executed the run. Surfaced on terminal
    events by MANTYX ≥ 2026-09. See ``docs/agent-runs-protocol.md``
    §7.1.

    ``provider`` being empty is the "no usage data" sentinel
    callers should match on against legacy MANTYX runners; when the
    server omits the cost-attribution triple entirely the SDK leaves
    :attr:`RunResult.model` at ``None`` instead.
    """

    # Catalog id — the same string a caller would pass back as
    # ``model_id`` to re-select this exact entry. Empty against
    # legacy fallbacks that didn't synthesise a catalog id.
    id: str = ""
    # Lowercase provider id: "openai" / "anthropic" / "google" /
    # "azure-openai". Empty against legacy runners.
    provider: str = ""
    # Vendor model id the platform actually sent to the provider
    # (e.g. "gpt-5.4-mini", "claude-opus-4-7").
    vendor_model_id: str = ""
    # "off" | "low" | "medium" | "high". None when the provider
    # doesn't expose a reasoning-level knob or the run didn't
    # request one.
    reasoning_effort: str | None = None


@dataclass(frozen=True)
class RunFeedbackResult:
    """Response from ``POST /agent-runs/:runId/feedback``. See §9a."""

    id: str
    verdict: Literal["UP", "DOWN"]
    target_kind: str
    agent_run_id: str


_RUN_FEEDBACK_EXPLANATION_MAX = 8000

_EVAL_TERMINAL_STATUSES = frozenset({"succeeded", "failed", "cancelled"})


@dataclass
class InlineEvalCaseSpec:
    input: dict[str, Any]
    name: str | None = None
    scorers: list[dict[str, Any]] | None = None
    tool_mocks: dict[str, Any] | None = None
    tags: list[str] | None = None


@dataclass
class InlineEvalDatasetSpec:
    cases: list[InlineEvalCaseSpec]
    name: str | None = None
    tool_mocks: dict[str, Any] | None = None


@dataclass
class AgentEvalOverrides:
    system_prompt: str | None = None
    system_prompt_append: str | None = None
    model: str | None = None
    llm_provider_id: str | None = None
    reasoning_level: Literal["low", "medium", "high"] | None = None
    disable_tools: bool | None = None
    tool_allowlist: list[str] | None = None
    disabled_mocks: list[str] | None = None


@dataclass
class EvalDatasetSummary:
    id: str
    name: str
    description: str | None
    case_count: int
    run_count: int
    created_at: str
    updated_at: str


@dataclass
class EvalDatasetList:
    datasets: list[EvalDatasetSummary]


@dataclass
class EvalCase:
    id: str
    name: str
    input: dict[str, Any]
    scorers: list[dict[str, Any]]
    tags: list[str]
    tool_mocks: dict[str, Any] | None
    created_at: str
    updated_at: str


@dataclass
class EvalDatasetDetail:
    id: str
    name: str
    description: str | None
    tool_mocks: dict[str, Any] | None
    cases: list[EvalCase]
    created_at: str
    updated_at: str


@dataclass
class EvalRunAccepted:
    run_id: str
    status: str
    stream_url: str


@dataclass
class EvalRunSummary:
    id: str
    dataset_id: str
    dataset_name: str
    agent_id: str | None
    inline_agent: bool
    status: str
    total_cases: int
    completed_cases: int
    passed_cases: int
    score: float | None
    token_usage: dict[str, Any] | None
    error: str | None
    agent_overrides: dict[str, Any] | None
    started_at: str | None
    finished_at: str | None
    created_at: str


@dataclass
class EvalCaseResult:
    id: str
    case_id: str
    case: EvalCase
    final_text: str | None
    tool_calls: list[dict[str, Any]]
    scores: list[dict[str, Any]]
    passed: bool
    score: float
    tokens: dict[str, Any] | None
    latency_ms: int | None
    error: str | None
    created_at: str


@dataclass
class EvalRunDetail(EvalRunSummary):
    inline_agent_spec: dict[str, Any] | None
    agent_spec_snapshot: dict[str, Any] | None
    updated_at: str
    results: list[EvalCaseResult]


@dataclass
class EvalRunList:
    runs: list[EvalRunSummary]
    limit: int
    offset: int


@dataclass
class EvalRunCompareSide:
    id: str
    agent_id: str | None
    inline_agent: bool
    status: str
    total_cases: int
    completed_cases: int
    passed_cases: int
    score: float | None
    token_usage: dict[str, Any] | None
    agent_overrides: dict[str, Any] | None
    agent_spec_snapshot: dict[str, Any] | None
    started_at: str | None
    finished_at: str | None
    created_at: str


@dataclass
class EvalRunCompareCase:
    case_id: str
    case_name: str
    case_input: dict[str, Any] | None
    a: dict[str, Any] | None
    b: dict[str, Any] | None


@dataclass
class EvalRunCompare:
    dataset_id: str
    dataset_name: str
    run_a: EvalRunCompareSide
    run_b: EvalRunCompareSide
    cases: list[EvalRunCompareCase]


@dataclass
class EvalRunEvent:
    type: str
    data: dict[str, Any] = field(default_factory=dict)


@dataclass
class RunResult:
    run_id: str
    text: str
    events: list[RunEvent]
    # Per-run token totals from the terminal event. ``None`` against
    # MANTYX servers older than 2026-09 (the "no usage data" signal
    # is ``result.model is None`` or ``result.model.provider == ""``).
    # See :class:`RunTokenUsage` and
    # ``docs/agent-runs-protocol.md`` §7.1.
    tokens: RunTokenUsage | None = None
    # Total ``engine.completeTurn(...)`` invocations for the run,
    # including the failing call when a run errored mid-loop. A
    # single-shot run reports ``1``; a tool loop is ``>= 2``.
    # ``None`` against legacy MANTYX servers.
    turns: int | None = None
    # Resolved model that executed the run. ``None`` against legacy
    # MANTYX servers.
    model: RunModelInfo | None = None
    # Final structured checklist for plan-only runs. ``None`` for normal
    # executed runs — use ``task_plan`` events for live progress.
    plan: TaskPlan | None = None


@dataclass
class SessionInfo:
    id: str
    name: str
    status: str
    created_at: str
    last_used_at: str
    ended_at: str | None
    agent_spec: AgentSpec
    messages: list[ConversationMessage]
    metadata: Metadata


@dataclass
class SessionSummary:
    """One row from :meth:`MantyxClient.list_sessions`."""

    session_id: str
    #: ISO 8601 creation timestamp.
    creation_date: str
    #: ISO 8601 timestamp of the most recent message run.
    last_interaction_date: str
    #: Best-effort label derived from the first user prompt (sessions have no title).
    summary: str
    metadata: dict[str, str]
    status: str


@dataclass
class SessionListResult:
    """Paginated result of :meth:`MantyxClient.list_sessions`."""

    total: int
    limit: int
    offset: int
    sessions: list[SessionSummary]


# --------------------------------------------------------------------- Client


class MantyxClient:
    """Synchronous MANTYX client.

    Example:
        >>> client = MantyxClient(api_key="...", workspace_slug="acme-corp")
        >>> result = client.run_agent(
        ...     system_prompt="You are a helpful assistant.",
        ...     prompt="What's the capital of France?",
        ... )
        >>> print(result.text)
    """

    def __init__(
        self,
        *,
        api_key: str | None = None,
        access_token: str | None = None,
        token_source: TokenSource | None = None,
        workspace_slug: str,
        base_url: str = DEFAULT_BASE_URL,
        timeout: float = DEFAULT_TIMEOUT_S,
        http_client: httpx.Client | None = None,
    ) -> None:
        """Construct the client.

        Pass exactly one of:

        - ``api_key`` — workspace API key (token prefix ``mantyx_``).
        - ``access_token`` — MANTYX OAuth 2.0 access token (token prefix
          ``mantyx_at_``). 1-hour-lived per ``docs/oauth.md`` — for
          long-running processes prefer ``token_source``.
        - ``token_source`` — dynamic credential provider that the SDK
          calls before every request, and again with
          ``reason="unauthorized"`` after a 401 (refresh + retry once).
          Build one with
          :meth:`MantyxOAuthClient.refresh_token_source` or
          :meth:`MantyxOAuthClient.client_credentials_token_source`,
          or supply any callable matching the :class:`TokenSource`
          protocol for full custom control (e.g. tokens minted by an
          upstream auth proxy).

        The server resolves either static credential by token-prefix
        sniffing, so the SDK only ships one
        ``Authorization: Bearer <credential>`` header. Exposing
        ``api_key`` / ``access_token`` separately just makes the call
        site self-documenting.

        See ``docs/agent-runs-protocol.md`` §2 for the credential table,
        scope semantics, and the ``insufficient_scope`` 403 surfaced as
        :class:`MantyxScopeError`.
        """
        credential, source = _resolve_credential(
            api_key=api_key, access_token=access_token, token_source=token_source
        )
        if not workspace_slug or not isinstance(workspace_slug, str):
            raise MantyxError("workspace_slug is required")
        # Kept as ``api_key`` for backwards compatibility — older
        # releases exposed it under this name on the client instance.
        # Empty when a ``token_source`` is configured.
        self.api_key = credential
        self.token_source: TokenSource | None = source
        self.workspace_slug = workspace_slug
        self.base_url = base_url.rstrip("/")
        self.timeout = timeout
        self._owns_http = http_client is None
        self._http = http_client or httpx.Client(
            timeout=httpx.Timeout(timeout, connect=10.0, read=None),
            headers={"User-Agent": f"mantyx-sdk-python/{SDK_VERSION}"},
        )
        # Lazily started on the first `mcp_local` use so apps that never use
        # MCP don't pay the daemon-thread cost. Closed by `close()`.
        self._mcp_portal = _SyncMcpPortal()

    # ------------------------------------------------------------------ ctx

    def __enter__(self) -> MantyxClient:
        return self

    def __exit__(self, *exc: Any) -> None:
        self.close()

    def close(self) -> None:
        if self._owns_http:
            self._http.close()
        self._mcp_portal.close()

    # --------------------------------------------------------------- Models

    def list_models(self) -> ModelCatalog:
        body = self._request("GET", "/models")
        return _parse_model_catalog(body or {})

    # -------------------------------------------------------------- Runs

    def run_agent(
        self,
        *,
        prompt: str | None = None,
        messages: Sequence[ConversationMessage] | None = None,
        attachments: Sequence[MessageAttachment] | None = None,
        system_prompt: str | None = None,
        agent_id: str | None = None,
        model_id: str | None = None,
        name: str | None = None,
        tools: Sequence[ToolRef] | None = None,
        reasoning_level: ReasoningLevel | None = None,
        output_schema: OutputSchema | None = None,
        loop_detection: LoopDetection | bool | None = _UNSET,
        tool_budgets: ToolBudgets | None = _UNSET,
        supervisor: Supervisor | bool | None = _UNSET,
        plan: PlanSpec | None = _UNSET,
        budgets: RunBudgets | None = None,
        metadata: Metadata | None = None,
        on_assistant_delta: Callable[[str], None] | None = None,
        on_event: Callable[[RunEvent], None] | None = None,
    ) -> RunResult:
        tools_list: list[ToolRef] | None = list(tools) if tools else None
        # Resolve every `a2a_local` agent card and open every `mcp_local`
        # transport before submitting; the resolver mutates the refs in
        # place so the subsequent `_serialize_agent_spec` reads the
        # resolved data.
        sync_resolve_local_refs(tools_list, http=self._http, portal=self._mcp_portal)
        try:
            body = _serialize_agent_spec(
                agent_id=agent_id,
                system_prompt=system_prompt,
                model_id=model_id,
                name=name,
                tools=tools_list,
                reasoning_level=reasoning_level,
                output_schema=output_schema,
                loop_detection=loop_detection,
                tool_budgets=tool_budgets,
                supervisor=supervisor,
                plan=plan,
                budgets=budgets,
                metadata=metadata,
                prompt=prompt,
                messages=messages,
                attachments=attachments,
            )

            created = self._request("POST", "/agent-runs", body)
            run_id = str((created or {}).get("runId") or "")
            if not run_id:
                raise MantyxError("server did not return a runId")
            handlers = collect_local_handlers(tools_list)
            return self._drive_run(
                run_id=run_id,
                handlers=handlers,
                on_assistant_delta=on_assistant_delta,
                on_event=on_event,
            )
        finally:
            # One-shot runs own their MCP transports; close on exit.
            sync_close_mcp_refs(tools_list)

    def run_plan(
        self,
        *,
        prompt: str | None = None,
        messages: Sequence[ConversationMessage] | None = None,
        attachments: Sequence[MessageAttachment] | None = None,
        system_prompt: str | None = None,
        agent_id: str | None = None,
        model_id: str | None = None,
        name: str | None = None,
        tools: Sequence[ToolRef] | None = None,
        reasoning_level: ReasoningLevel | None = None,
        output_schema: OutputSchema | None = None,
        loop_detection: LoopDetection | bool | None = _UNSET,
        tool_budgets: ToolBudgets | None = _UNSET,
        supervisor: Supervisor | bool | None = _UNSET,
        budgets: RunBudgets | None = None,
        metadata: Metadata | None = None,
        steps: Sequence[str] | None = None,
        brief: str | None = None,
        on_assistant_delta: Callable[[str], None] | None = None,
        on_event: Callable[[RunEvent], None] | None = None,
    ) -> RunResult:
        """Plan-only run: classify (or accept caller ``steps``) and return
        the structured checklist without executing the agent loop."""
        return self.run_agent(
            prompt=prompt,
            messages=messages,
            attachments=attachments,
            system_prompt=system_prompt,
            agent_id=agent_id,
            model_id=model_id,
            name=name,
            tools=tools,
            reasoning_level=reasoning_level,
            output_schema=output_schema,
            loop_detection=loop_detection,
            tool_budgets=tool_budgets,
            supervisor=supervisor,
            plan=plan_only(steps=list(steps) if steps is not None else None, brief=brief),
            budgets=budgets,
            metadata=metadata,
            on_assistant_delta=on_assistant_delta,
            on_event=on_event,
        )

    def stream_agent(
        self,
        *,
        prompt: str | None = None,
        messages: Sequence[ConversationMessage] | None = None,
        attachments: Sequence[MessageAttachment] | None = None,
        system_prompt: str | None = None,
        agent_id: str | None = None,
        model_id: str | None = None,
        name: str | None = None,
        tools: Sequence[ToolRef] | None = None,
        reasoning_level: ReasoningLevel | None = None,
        output_schema: OutputSchema | None = None,
        loop_detection: LoopDetection | bool | None = _UNSET,
        tool_budgets: ToolBudgets | None = _UNSET,
        supervisor: Supervisor | bool | None = _UNSET,
        plan: PlanSpec | None = _UNSET,
        budgets: RunBudgets | None = None,
        metadata: Metadata | None = None,
    ) -> Iterator[RunEvent]:
        tools_list: list[ToolRef] | None = list(tools) if tools else None
        sync_resolve_local_refs(tools_list, http=self._http, portal=self._mcp_portal)
        body = _serialize_agent_spec(
            agent_id=agent_id,
            system_prompt=system_prompt,
            model_id=model_id,
            name=name,
            tools=tools_list,
            reasoning_level=reasoning_level,
            output_schema=output_schema,
            loop_detection=loop_detection,
            tool_budgets=tool_budgets,
            supervisor=supervisor,
            plan=plan,
            budgets=budgets,
            metadata=metadata,
            prompt=prompt,
            messages=messages,
            attachments=attachments,
        )

        created = self._request("POST", "/agent-runs", body)
        run_id = str((created or {}).get("runId") or "")
        if not run_id:
            sync_close_mcp_refs(tools_list)
            raise MantyxError("server did not return a runId")
        handlers = collect_local_handlers(tools_list)

        def _gen() -> Iterator[RunEvent]:
            try:
                yield from self._stream_events(run_id, handlers)
            finally:
                sync_close_mcp_refs(tools_list)

        return _gen()

    def cancel_run(self, run_id: str) -> None:
        self._request("POST", f"/agent-runs/{_quote(run_id)}/cancel")

    def submit_run_feedback(
        self,
        run_id: str,
        verdict: Literal["UP", "DOWN"],
        *,
        explanation: str | None = None,
        content_snapshot: str | None = None,
    ) -> RunFeedbackResult:
        """Record thumbs up/down feedback on a run.

        Requires the ``feedback:write`` OAuth scope (workspace API keys have
        implicit access). Idempotent per run — the first call returns HTTP
        201, updates return 200. See ``docs/agent-runs-protocol.md`` §9a.
        """
        if verdict not in ("UP", "DOWN"):
            raise MantyxError(f'feedback verdict must be "UP" or "DOWN", got {verdict!r}')
        if explanation is not None and len(explanation) > _RUN_FEEDBACK_EXPLANATION_MAX:
            raise MantyxError(
                f"feedback explanation must be <= {_RUN_FEEDBACK_EXPLANATION_MAX} characters"
            )
        wire: dict[str, str] = {"verdict": verdict}
        if explanation is not None:
            wire["explanation"] = explanation
        if content_snapshot is not None:
            wire["contentSnapshot"] = content_snapshot
        data = self._request("POST", f"/agent-runs/{_quote(run_id)}/feedback", wire) or {}
        return _parse_run_feedback(data, run_id)

    # --------------------------------------------------------------- Evals

    def list_eval_datasets(self) -> EvalDatasetList:
        body = self._request("GET", "/eval-datasets")
        return _parse_eval_dataset_list(body or {})

    def get_eval_dataset(self, dataset_id: str) -> EvalDatasetDetail:
        body = self._request("GET", f"/eval-datasets/{_quote(dataset_id)}")
        return _parse_eval_dataset_detail(body or {})

    def create_eval_run(
        self,
        *,
        dataset_id: str | None = None,
        dataset: InlineEvalDatasetSpec | Mapping[str, Any] | None = None,
        agent_id: str | None = None,
        agent: Mapping[str, Any] | None = None,
        tools: Sequence[ToolRef] | None = None,
        model_id: str | None = None,
        overrides: AgentEvalOverrides | Mapping[str, Any] | None = None,
    ) -> EvalRunAccepted:
        tools_list = _coerce_eval_tools(agent, tools)
        sync_resolve_local_refs(tools_list, http=self._http, portal=self._mcp_portal)
        wire = _serialize_create_eval_run(
            dataset_id=dataset_id,
            dataset=dataset,
            agent_id=agent_id,
            agent=agent,
            tools=tools_list,
            model_id=model_id,
            overrides=overrides,
        )
        body = self._request("POST", "/eval-runs", wire)
        return _parse_eval_run_accepted(body or {})

    def list_eval_runs(
        self,
        *,
        dataset_id: str | None = None,
        agent_id: str | None = None,
        status: str | None = None,
        limit: int | None = None,
        offset: int | None = None,
    ) -> EvalRunList:
        params: dict[str, Any] = {}
        if dataset_id is not None:
            params["datasetId"] = dataset_id
        if agent_id is not None:
            params["agentId"] = agent_id
        if status is not None:
            params["status"] = status
        if limit is not None:
            params["limit"] = limit
        if offset is not None:
            params["offset"] = offset
        body = self._request("GET", "/eval-runs", params=params or None)
        return _parse_eval_run_list(body or {})

    def compare_eval_runs(self, run_a: str, run_b: str) -> EvalRunCompare:
        body = self._request("GET", "/eval-runs/compare", params={"a": run_a, "b": run_b})
        return _parse_eval_run_compare(body or {})

    def get_eval_run(self, run_id: str) -> EvalRunDetail:
        body = self._request("GET", f"/eval-runs/{_quote(run_id)}")
        return _parse_eval_run_detail(body or {})

    def stream_eval_run(self, run_id: str) -> Iterator[EvalRunEvent]:
        url = self._absolute_url(f"/eval-runs/{_quote(run_id)}/stream")
        for attempt_reason in ("initial", "unauthorized"):
            headers = self._auth_headers(attempt_reason)
            headers["Accept"] = "text/event-stream"
            with self._http.stream("GET", url, headers=headers, timeout=None) as resp:
                if (
                    resp.status_code == 401
                    and self.token_source is not None
                    and attempt_reason == "initial"
                ):
                    continue
                if resp.status_code != 200:
                    self._raise_for_status(resp)
                for frame in iter_sse(resp.iter_bytes()):
                    if not frame.data:
                        continue
                    try:
                        raw = json.loads(frame.data)
                    except json.JSONDecodeError as exc:
                        raise MantyxError(f"invalid eval SSE JSON: {frame.data[:200]}") from exc
                    ev = _parse_eval_run_event(raw)
                    yield ev
                    if ev.type in ("run_completed", "run_error", "run_cancelled"):
                        return
                return

    def cancel_eval_run(self, run_id: str) -> dict[str, bool]:
        body = self._request("POST", f"/eval-runs/{_quote(run_id)}/cancel")
        return {"ok": bool((body or {}).get("ok"))}

    def run_eval(
        self,
        *,
        dataset_id: str | None = None,
        dataset: InlineEvalDatasetSpec | Mapping[str, Any] | None = None,
        agent_id: str | None = None,
        agent: Mapping[str, Any] | None = None,
        tools: Sequence[ToolRef] | None = None,
        model_id: str | None = None,
        overrides: AgentEvalOverrides | Mapping[str, Any] | None = None,
        on_event: Callable[[EvalRunEvent], None] | None = None,
        poll_interval_s: float = 1.0,
    ) -> EvalRunDetail:
        tools_list = _coerce_eval_tools(agent, tools)
        sync_resolve_local_refs(tools_list, http=self._http, portal=self._mcp_portal)
        try:
            accepted = self.create_eval_run(
                dataset_id=dataset_id,
                dataset=dataset,
                agent_id=agent_id,
                agent=agent,
                tools=tools_list,
                model_id=model_id,
                overrides=overrides,
            )
            handlers = collect_local_handlers(tools_list)
            stream_thread: threading.Thread | None = None

            if tools_list or on_event is not None:

                def _consume_stream() -> None:
                    try:
                        with ThreadPoolExecutor(
                            max_workers=4, thread_name_prefix="mantyx-eval-tool"
                        ) as pool:
                            for ev in self.stream_eval_run(accepted.run_id):
                                if ev.type == "local_tool_call" and tools_list:
                                    agent_run_id = str(ev.data.get("agentRunId") or "")
                                    if agent_run_id:
                                        pool.submit(
                                            self._dispatch_local_tool,
                                            agent_run_id,
                                            _eval_event_to_run_event(ev),
                                            handlers,
                                        )
                                if on_event is not None:
                                    on_event(ev)
                    except MantyxNetworkError:
                        pass

                stream_thread = threading.Thread(target=_consume_stream, daemon=True)
                stream_thread.start()

            while True:
                detail = self.get_eval_run(accepted.run_id)
                if detail.status in _EVAL_TERMINAL_STATUSES:
                    if stream_thread is not None:
                        stream_thread.join(timeout=30.0)
                    return detail
                time.sleep(poll_interval_s)
        finally:
            sync_close_mcp_refs(tools_list)

    # ----------------------------------------------------------- Sessions

    def create_session(
        self,
        *,
        system_prompt: str | None = None,
        agent_id: str | None = None,
        model_id: str | None = None,
        name: str | None = None,
        tools: Sequence[ToolRef] | None = None,
        reasoning_level: ReasoningLevel | None = None,
        output_schema: OutputSchema | None = None,
        loop_detection: LoopDetection | bool | None = _UNSET,
        tool_budgets: ToolBudgets | None = _UNSET,
        supervisor: Supervisor | bool | None = _UNSET,
        plan: PlanSpec | None = _UNSET,
        budgets: RunBudgets | None = None,
        metadata: Metadata | None = None,
    ) -> AgentSession:
        tools_list: list[ToolRef] | None = list(tools) if tools else None
        # Resolve once at session creation; the session keeps the resolved
        # cards / live MCP connections for its lifetime.
        sync_resolve_local_refs(tools_list, http=self._http, portal=self._mcp_portal)
        try:
            body = _serialize_agent_spec(
                agent_id=agent_id,
                system_prompt=system_prompt,
                model_id=model_id,
                name=name,
                tools=tools_list,
                reasoning_level=reasoning_level,
                output_schema=output_schema,
                loop_detection=loop_detection,
                tool_budgets=tool_budgets,
                supervisor=supervisor,
                plan=plan,
                budgets=budgets,
                metadata=metadata,
            )
            created = self._request("POST", "/agent-sessions", body) or {}
        except Exception:
            sync_close_mcp_refs(tools_list)
            raise
        session_id = str(created.get("sessionId") or "")
        if not session_id:
            sync_close_mcp_refs(tools_list)
            raise MantyxError("server did not return a sessionId")
        handlers = collect_local_handlers(tools_list)
        return AgentSession(self, id=session_id, handlers=handlers, tools_for_resume=tools_list)

    def resume_session(
        self,
        session_id: str,
        *,
        tools: Sequence[ToolRef] | None = None,
    ) -> AgentSession:
        # Verify the session exists.
        self.get_session_info(session_id)
        tools_list: list[ToolRef] | None = list(tools) if tools else None
        if tools_list:
            sync_resolve_local_refs(tools_list, http=self._http, portal=self._mcp_portal)
        handlers = collect_local_handlers(tools_list)
        return AgentSession(self, id=session_id, handlers=handlers, tools_for_resume=tools_list)

    def end_session(self, session_id: str) -> None:
        self._request("DELETE", f"/agent-sessions/{_quote(session_id)}")

    def get_session_info(self, session_id: str) -> SessionInfo:
        body = self._request("GET", f"/agent-sessions/{_quote(session_id)}") or {}
        return _parse_session_info(body)

    def list_sessions(
        self,
        *,
        metadata: Metadata | None = None,
        status: str | None = None,
        limit: int | None = None,
        offset: int | None = None,
    ) -> SessionListResult:
        """List the workspace's sessions, most-recently-used first.

        Filter by the ``metadata`` you attached at create time to find earlier
        sessions by your own identifiers (customer id, environment, …).
        Multiple metadata entries are AND-combined server-side.
        """
        params: dict[str, Any] = {}
        if metadata:
            params["metadata"] = [f"{k}:{v}" for k, v in metadata.items()]
        if status:
            params["status"] = status
        if limit is not None:
            params["limit"] = limit
        if offset is not None:
            params["offset"] = offset
        body = self._request("GET", "/agent-sessions", params=params) or {}
        return _parse_session_list(body)

    def get_session_events(
        self,
        session_id: str,
        *,
        full: bool = False,
        last_messages: int | None = None,
    ) -> list[RunEvent]:
        """Fetch a session's conversation as realtime-style event frames.

        Returns ``user_message`` / ``assistant_message`` frames (see the wire
        protocol §6.2) so a UI can restore the thread through the same handler
        it uses for the live stream. Pass ``last_messages`` to fetch only the
        most recent turns, or ``full=True`` for the entire history (default).
        """
        params: dict[str, Any] = {}
        if full:
            params["full"] = "1"
        elif last_messages is not None:
            params["lastMessages"] = last_messages
        body = (
            self._request("GET", f"/agent-sessions/{_quote(session_id)}/events", params=params)
            or {}
        )
        return _parse_session_events(body)

    # ------------------------------------------------------------ Internals

    def _drive_run(
        self,
        *,
        run_id: str,
        handlers: _LocalHandlers,
        on_assistant_delta: Callable[[str], None] | None = None,
        on_event: Callable[[RunEvent], None] | None = None,
    ) -> RunResult:
        collected: list[RunEvent] = []
        final_text = ""
        # Cost-attribution triple from `docs/agent-runs-protocol.md`
        # §7.1. Populated when MANTYX ≥ 2026-09 surfaces it on the
        # terminal event; left as ``None`` against legacy runners so
        # callers can detect "no usage data" via ``result.model is
        # None``.
        tokens: RunTokenUsage | None = None
        turns: int | None = None
        model_info: RunModelInfo | None = None
        task_plan: TaskPlan | None = None
        for ev in self._stream_events(run_id, handlers):
            collected.append(ev)
            if on_event is not None:
                on_event(ev)
            if ev.type == "assistant_delta" and on_assistant_delta is not None:
                t = ev.data.get("text")
                if isinstance(t, str):
                    on_assistant_delta(t)
            if ev.type == "result":
                subtype = str(ev.data.get("subtype") or "")
                parsed_tokens = _parse_run_tokens(ev.data.get("tokens"))
                if parsed_tokens is not None:
                    tokens = parsed_tokens
                parsed_turns = _parse_run_turns(ev.data.get("turns"))
                if parsed_turns is not None:
                    turns = parsed_turns
                parsed_model = _parse_run_model(ev.data.get("model"))
                if parsed_model is not None:
                    model_info = parsed_model
                if subtype == "success":
                    txt = ev.data.get("text")
                    final_text = txt if isinstance(txt, str) else ""
                    parsed_plan = parse_task_plan(ev.data.get("plan"))
                    if parsed_plan is not None:
                        task_plan = parsed_plan
                else:
                    msg = ev.data.get("error") or subtype or "run failed"
                    raise MantyxRunError(
                        run_id,
                        subtype or "error",
                        str(msg),
                        tokens=tokens,
                        turns=turns,
                        model=model_info,
                    )
            elif ev.type == "error":
                # The wire reports both a coarse `code` (legacy alias)
                # and a canonical `errorClass` triage category; prefer
                # `errorClass` for the run-error subtype when present so
                # callers see a stable taxonomy. See
                # `docs/agent-runs-protocol.md` §7.
                error_class_raw = ev.data.get("errorClass")
                error_class = str(error_class_raw) if isinstance(error_class_raw, str) else None
                subtype = error_class or str(ev.data.get("code") or "error")
                finish_raw = ev.data.get("finishReason")
                finish_reason = str(finish_raw) if isinstance(finish_raw, str) else None
                partial_raw = ev.data.get("partialText")
                partial_text = str(partial_raw) if isinstance(partial_raw, str) else None
                retryable_raw = ev.data.get("retryable")
                retryable = retryable_raw if isinstance(retryable_raw, bool) else None
                # Failed runs against MANTYX ≥ 2026-09 also carry the
                # cost-attribution triple — the failing model call's
                # usage is included. See §7.1.
                err_tokens = _parse_run_tokens(ev.data.get("tokens"))
                err_turns = _parse_run_turns(ev.data.get("turns"))
                err_model = _parse_run_model(ev.data.get("model"))
                raise MantyxRunError(
                    run_id,
                    subtype,
                    str(ev.data.get("error") or "error"),
                    error_class=error_class,
                    finish_reason=finish_reason,
                    partial_text=partial_text,
                    retryable=retryable,
                    tokens=err_tokens,
                    turns=err_turns,
                    model=err_model,
                )
            elif ev.type == "cancelled":
                raise MantyxRunError(run_id, "cancelled", "Run was cancelled")
        return RunResult(
            run_id=run_id,
            text=final_text,
            events=collected,
            tokens=tokens,
            turns=turns,
            model=model_info,
            plan=task_plan,
        )

    def _stream_events(
        self,
        run_id: str,
        handlers: _LocalHandlers,
    ) -> Iterator[RunEvent]:
        """Open the SSE stream and yield typed events. Reconnects on
        non-terminal disconnects via ``Last-Event-ID`` + ``?lastSeq=``.

        At-most-one refresh + retry on 401 for the initial open when a
        ``token_source`` is configured. Mid-stream 401s drop into the
        ``except`` reconnect path as normal network blips.
        """
        last_seq = 0
        # Tool dispatch happens off-thread so the stream consumer keeps reading.
        with ThreadPoolExecutor(max_workers=4, thread_name_prefix="mantyx-tool") as pool:
            while True:
                terminal_seen = False
                url = self._absolute_url(f"/agent-runs/{_quote(run_id)}/stream")
                params: dict[str, Any] = {}
                if last_seq > 0:
                    params["lastSeq"] = last_seq
                try:
                    for attempt_reason in ("initial", "unauthorized"):
                        headers = self._auth_headers(attempt_reason)
                        headers["Accept"] = "text/event-stream"
                        if last_seq > 0:
                            headers["Last-Event-ID"] = str(last_seq)
                        with self._http.stream(
                            "GET", url, params=params, headers=headers, timeout=None
                        ) as resp:
                            if (
                                resp.status_code == 401
                                and self.token_source is not None
                                and attempt_reason == "initial"
                            ):
                                # Refresh + retry once.
                                continue
                            if resp.status_code != 200:
                                self._raise_for_status(resp)
                            for sse_ev in iter_sse(resp.iter_bytes()):
                                ev = _to_run_event(sse_ev, last_seq)
                                if ev.seq > last_seq:
                                    last_seq = ev.seq
                                yield ev
                                if ev.type == "local_tool_call":
                                    pool.submit(self._dispatch_local_tool, run_id, ev, handlers)
                                if ev.type in ("result", "error", "cancelled"):
                                    terminal_seen = True
                                    break
                            break  # successful open path; don't try "unauthorized"
                except httpx.HTTPError:  # network blip — retry
                    if terminal_seen:
                        return
                    time.sleep(0.5)
                    continue
                if terminal_seen:
                    return
                # Stream closed without a terminal event — reconnect.
                continue

    def _dispatch_local_tool(
        self,
        run_id: str,
        ev: RunEvent,
        handlers: _LocalHandlers,
    ) -> None:
        name = str(ev.data.get("name") or "")
        tool_use_id = str(ev.data.get("toolUseId") or "")
        if not tool_use_id:
            return
        kind = str(ev.data.get("kind") or "local")
        try:
            if kind == "a2a_local":
                a2a = handlers.a2a_tools.get(name)
                if a2a is None:
                    self._post_tool_result(
                        run_id,
                        tool_use_id,
                        error=f"No local A2A handler registered for tool {name!r}",
                    )
                    return
                args = ev.data.get("args") or {}
                message = ""
                if isinstance(args, dict):
                    raw_msg = args.get("message")
                    message = raw_msg if isinstance(raw_msg, str) else ""
                text = sync_call_a2a(a2a, message, http=self._http)
                self._post_tool_result(run_id, tool_use_id, result=text)
                return
            if kind == "mcp_local":
                server_name = str(ev.data.get("mcpServer") or "")
                tool_name = str(ev.data.get("mcpToolName") or "")
                server = handlers.mcp_servers.get(server_name)
                if server is None or server._resolved is None or server._resolved.call_sync is None:
                    self._post_tool_result(
                        run_id,
                        tool_use_id,
                        error=f"No local MCP server registered as {server_name!r}",
                    )
                    return
                # The wire-prefixed tool name (`<server>_<tool>`) is what the
                # model sees; the upstream MCP server uses the bare name.
                # Strip the prefix before forwarding to `tools/call`.
                upstream_name = (
                    tool_name[len(server_name) + 1 :]
                    if tool_name.startswith(f"{server_name}_")
                    else tool_name
                )
                args_in = ev.data.get("args") or ev.data.get("input") or {}
                args_dict: dict[str, Any] = (
                    cast(dict[str, Any], args_in) if isinstance(args_in, dict) else {}
                )
                text = server._resolved.call_sync(upstream_name, args_dict)
                self._post_tool_result(run_id, tool_use_id, result=text)
                return
            handler = handlers.local_tools.get(name)
            if handler is None:
                self._post_tool_result(
                    run_id, tool_use_id, error=f"No local handler registered for tool {name!r}"
                )
                return
            args = parse_args_with_pydantic(
                handler.parameters,
                cast(dict[str, Any] | None, ev.data.get("args") or ev.data.get("input")),
            )
            from .tools import call_handler_sync, normalize_local_tool_output

            out = call_handler_sync(handler.execute, args)
            text, files = normalize_local_tool_output(out)
            self._post_tool_result(run_id, tool_use_id, result=text, files=files)
        except Exception as e:
            self._post_tool_result(
                run_id,
                tool_use_id,
                error=MantyxToolError(_describe_handler(ev, name), str(e)).message,
            )

    def _post_tool_result(
        self,
        run_id: str,
        tool_use_id: str,
        *,
        result: str | None = None,
        error: str | None = None,
        files: list[dict[str, str]] | None = None,
    ) -> None:
        body: dict[str, Any] = {"toolUseId": tool_use_id}
        if result is not None:
            body["result"] = result
        if error is not None:
            body["error"] = error
        if files:
            body["files"] = files
        path = f"/agent-runs/{_quote(run_id)}/tool-results"
        for attempt in range(_TOOL_RESULT_POST_MAX_ATTEMPTS):
            try:
                self._request("POST", path, body)
                return
            except MantyxError as exc:
                if (
                    attempt == _TOOL_RESULT_POST_MAX_ATTEMPTS - 1
                    or not _is_tool_result_post_retryable(exc)
                ):
                    # The server will time-out the tool-use and surface the right
                    # terminal event on the SSE stream.
                    return
                time.sleep(_tool_result_post_backoff_s(attempt))

    # --------------------------------------------------------------- HTTP

    def _absolute_url(self, path: str) -> str:
        return f"{self.base_url}/api/v1/workspaces/{_quote(self.workspace_slug)}{path}"

    def _resolve_bearer(self, reason: str = "initial") -> str:
        """Resolve the bearer credential to send on the next request.

        Static ``api_key`` / ``access_token`` clients reach into the
        cached value; ``token_source`` clients delegate so the source
        can refresh expired access tokens before we hit the wire. Pass
        ``reason="unauthorized"`` immediately after a 401 to force a
        refresh.
        """
        if self.token_source is not None:
            return self.token_source(reason)  # type: ignore[arg-type]
        return self.api_key

    def _auth_headers(self, reason: str = "initial") -> dict[str, str]:
        return {"Authorization": f"Bearer {self._resolve_bearer(reason)}"}

    def _request(
        self,
        method: str,
        path: str,
        body: Mapping[str, Any] | None = None,
        *,
        params: Mapping[str, Any] | None = None,
    ) -> dict[str, Any] | None:
        return self._request_with_retry(method, path, body, reason="initial", params=params)

    def _request_with_retry(
        self,
        method: str,
        path: str,
        body: Mapping[str, Any] | None,
        reason: str,
        params: Mapping[str, Any] | None = None,
    ) -> dict[str, Any] | None:
        url = self._absolute_url(path)
        headers = self._auth_headers(reason)
        headers["Accept"] = "application/json"
        request_kwargs: dict[str, Any] = {"method": method, "url": url, "headers": headers}
        if params:
            request_kwargs["params"] = params
        if body is not None:
            request_kwargs["json"] = body
            headers["Content-Type"] = "application/json"
        try:
            resp = self._http.request(**request_kwargs)
        except httpx.HTTPError as exc:
            raise MantyxNetworkError(str(exc), cause=exc) from exc
        # 401 with a configured token_source: refresh the access token
        # and retry the original request exactly once. Static-credential
        # clients fall through to ``MantyxAuthError`` as before.
        if resp.status_code == 401 and self.token_source is not None and reason == "initial":
            return self._request_with_retry(
                method, path, body, reason="unauthorized", params=params
            )
        if resp.status_code >= 400:
            self._raise_for_status(resp)
        text = resp.text
        if not text:
            return None
        try:
            data = resp.json()
        except ValueError as exc:
            raise MantyxError(f"Failed to parse JSON response: {exc}") from exc
        if isinstance(data, dict):
            return cast(dict[str, Any], data)
        return None

    def _raise_for_status(self, resp: httpx.Response) -> None:
        body: dict[str, Any] = {}
        try:
            body = resp.json() or {}
        except Exception:
            pass
        message = str(body.get("error") or body.get("message") or f"HTTP {resp.status_code}")
        code = str(body.get("code") or body.get("error") or f"http_{resp.status_code}")
        hint_raw = body.get("hint")
        hint = hint_raw if isinstance(hint_raw, str) else None
        if resp.status_code == 401:
            raise MantyxAuthError(message)
        # `403 insufficient_scope` is the OAuth "missing scope" signal.
        # See `docs/agent-runs-protocol.md` §2.3.
        if resp.status_code == 403 and (
            body.get("error") == "insufficient_scope" or body.get("code") == "insufficient_scope"
        ):
            required = _parse_required_scopes(
                body.get("required"),
                resp.headers.get("WWW-Authenticate"),
            )
            scope_msg = (
                f"Missing OAuth scope{'s' if len(required) > 1 else ''}: {', '.join(required)}"
                if required
                else "OAuth access token is missing a required scope"
            )
            raise MantyxScopeError(scope_msg, required_scopes=required)
        raise MantyxError(message, code=code, status=resp.status_code, hint=hint)


# -------------------------------------------------------------------- Session


class AgentSession:
    """Multi-turn conversation handle. The server owns the message history;
    the SDK holds the local-tool handlers in memory."""

    def __init__(
        self,
        client: MantyxClient,
        *,
        id: str,
        handlers: _LocalHandlers,
        tools_for_resume: list[ToolRef] | None = None,
    ) -> None:
        self.client = client
        self.id = id
        self._handlers = handlers
        self._tools_for_resume = tools_for_resume

    def send(
        self,
        prompt_or_messages: str | Sequence[ConversationMessage],
        *,
        attachments: Sequence[MessageAttachment] | None = None,
        metadata: Metadata | None = None,
        reasoning_level: ReasoningLevel | None = None,
        output_schema: OutputSchema | None = None,
        loop_detection: LoopDetection | bool | None = _UNSET,
        tool_budgets: ToolBudgets | None = _UNSET,
        supervisor: Supervisor | bool | None = _UNSET,
        plan: PlanSpec | None = _UNSET,
        on_assistant_delta: Callable[[str], None] | None = None,
        on_event: Callable[[RunEvent], None] | None = None,
    ) -> RunResult:
        body = self._build_message_body(
            prompt_or_messages,
            attachments=attachments,
            metadata=metadata,
            reasoning_level=reasoning_level,
            output_schema=output_schema,
            loop_detection=loop_detection,
            tool_budgets=tool_budgets,
            supervisor=supervisor,
            plan=plan,
        )
        created = (
            self.client._request("POST", f"/agent-sessions/{_quote(self.id)}/messages", body) or {}
        )
        run_id = str(created.get("runId") or "")
        if not run_id:
            raise MantyxError("server did not return a runId")
        return self.client._drive_run(
            run_id=run_id,
            handlers=self._handlers,
            on_assistant_delta=on_assistant_delta,
            on_event=on_event,
        )

    def stream(
        self,
        prompt_or_messages: str | Sequence[ConversationMessage],
        *,
        attachments: Sequence[MessageAttachment] | None = None,
        metadata: Metadata | None = None,
        reasoning_level: ReasoningLevel | None = None,
        output_schema: OutputSchema | None = None,
        loop_detection: LoopDetection | bool | None = _UNSET,
        tool_budgets: ToolBudgets | None = _UNSET,
        supervisor: Supervisor | bool | None = _UNSET,
        plan: PlanSpec | None = _UNSET,
    ) -> Iterator[RunEvent]:
        body = self._build_message_body(
            prompt_or_messages,
            attachments=attachments,
            metadata=metadata,
            reasoning_level=reasoning_level,
            output_schema=output_schema,
            loop_detection=loop_detection,
            tool_budgets=tool_budgets,
            supervisor=supervisor,
            plan=plan,
        )
        created = (
            self.client._request("POST", f"/agent-sessions/{_quote(self.id)}/messages", body) or {}
        )
        run_id = str(created.get("runId") or "")
        if not run_id:
            raise MantyxError("server did not return a runId")
        return self.client._stream_events(run_id, self._handlers)

    def run_plan(
        self,
        prompt_or_messages: str | Sequence[ConversationMessage],
        *,
        metadata: Metadata | None = None,
        reasoning_level: ReasoningLevel | None = None,
        output_schema: OutputSchema | None = None,
        loop_detection: LoopDetection | bool | None = _UNSET,
        tool_budgets: ToolBudgets | None = _UNSET,
        supervisor: Supervisor | bool | None = _UNSET,
        steps: Sequence[str] | None = None,
        brief: str | None = None,
        on_assistant_delta: Callable[[str], None] | None = None,
        on_event: Callable[[RunEvent], None] | None = None,
    ) -> RunResult:
        """Plan-only session turn without executing the agent loop."""
        return self.send(
            prompt_or_messages,
            metadata=metadata,
            reasoning_level=reasoning_level,
            output_schema=output_schema,
            loop_detection=loop_detection,
            tool_budgets=tool_budgets,
            supervisor=supervisor,
            plan=plan_only(steps=list(steps) if steps is not None else None, brief=brief),
            on_assistant_delta=on_assistant_delta,
            on_event=on_event,
        )

    def _build_message_body(
        self,
        prompt_or_messages: str | Sequence[ConversationMessage],
        *,
        attachments: Sequence[MessageAttachment] | None = None,
        metadata: Metadata | None,
        reasoning_level: ReasoningLevel | None,
        output_schema: OutputSchema | None = None,
        loop_detection: LoopDetection | bool | None = _UNSET,
        tool_budgets: ToolBudgets | None = _UNSET,
        supervisor: Supervisor | bool | None = _UNSET,
        plan: PlanSpec | None = _UNSET,
    ) -> dict[str, Any]:
        if isinstance(prompt_or_messages, str):
            body = _serialize_turn_input(
                prompt=prompt_or_messages,
                attachments=attachments,
            )
        else:
            body = _serialize_turn_input(messages=prompt_or_messages)
        if self._tools_for_resume:
            body["tools"] = serialize_tool_refs(self._tools_for_resume)
        if metadata:
            body["metadata"] = dict(metadata)
        normalized = normalize_reasoning_level(reasoning_level)
        if normalized is not None:
            body["reasoningLevel"] = normalized
        normalized_schema = normalize_output_schema(output_schema)
        if normalized_schema is not None:
            body["outputSchema"] = normalized_schema
        if loop_detection is not _UNSET:
            normalized_loop = normalize_loop_detection(loop_detection)
            if normalized_loop is not None:
                body["loopDetection"] = normalized_loop
        if tool_budgets is not _UNSET:
            normalized_budgets = normalize_tool_budgets(tool_budgets)
            if normalized_budgets is not None:
                body["toolBudgets"] = normalized_budgets
        if supervisor is not _UNSET:
            normalized_supervisor = normalize_supervisor(supervisor)
            if normalized_supervisor is not None:
                body["supervisor"] = normalized_supervisor
        if plan is not _UNSET:
            normalized_plan = normalize_plan(plan)
            if normalized_plan is not None:
                body["plan"] = normalized_plan
        return body

    def history(self) -> list[ConversationMessage]:
        info = self.client.get_session_info(self.id)
        return info.messages

    def info(self) -> SessionInfo:
        return self.client.get_session_info(self.id)

    def events(self, *, full: bool = False, last_messages: int | None = None) -> list[RunEvent]:
        """Replay this session's conversation as realtime-style event frames."""
        return self.client.get_session_events(self.id, full=full, last_messages=last_messages)

    def end(self) -> None:
        try:
            self.client.end_session(self.id)
        finally:
            # Close any MCP transports the session opened.
            sync_close_mcp_refs(self._tools_for_resume)


# -------------------------------------------------------------------- Helpers


def _resolve_credential(
    *,
    api_key: str | None,
    access_token: str | None,
    token_source: Any | None = None,
) -> tuple[str, Any | None]:
    """Pick exactly one of ``api_key`` / ``access_token`` / ``token_source``
    and return ``(credential, source)``.

    ``api_key`` and ``access_token`` are both static workspace bearers
    — the server resolves whichever token it sees by token-prefix
    sniffing, so they share a single header code path. ``token_source``
    is the dynamic alternative the HTTP layer calls before every
    request and on 401 retries; it is mutually exclusive with the
    static options because mixing them would obscure where the
    credential actually came from.
    """
    api_key_val = api_key if isinstance(api_key, str) else ""
    access_token_val = access_token if isinstance(access_token, str) else ""
    source = token_source if callable(token_source) else None
    provided = [
        name
        for name, present in (
            ("api_key", bool(api_key_val)),
            ("access_token", bool(access_token_val)),
            ("token_source", source is not None),
        )
        if present
    ]
    if len(provided) > 1:
        raise MantyxError(
            "Pass exactly one of `api_key`, `access_token`, or `token_source` — got "
            + " + ".join(provided)
        )
    if not provided:
        raise MantyxError(
            "One of `api_key` (workspace API key), `access_token` (OAuth access token), "
            "or `token_source` (dynamic credential provider) is required"
        )
    credential = api_key_val or access_token_val
    return credential, source


def _parse_required_scopes(
    body_required: Any,
    www_authenticate: str | None,
) -> tuple[str, ...]:
    """Extract the list of scopes the server reported as required for a
    route, from either the response body's ``required`` field or the
    ``WWW-Authenticate: Bearer error="insufficient_scope", scope="…"``
    header (RFC 6750).
    """
    if isinstance(body_required, list):
        return tuple(s for s in body_required if isinstance(s, str) and s)
    if isinstance(body_required, str) and body_required:
        return (body_required,)
    if isinstance(www_authenticate, str):
        import re

        m = re.search(r'scope="([^"]+)"', www_authenticate, re.IGNORECASE)
        if m:
            return tuple(s for s in m.group(1).split() if s)
    return ()


_TOOL_RESULT_POST_MAX_ATTEMPTS = 6


def _tool_result_post_backoff_s(attempt: int) -> float:
    return float(min(0.5 * (2**attempt), 8.0))


def _is_tool_result_post_retryable(exc: MantyxError) -> bool:
    if isinstance(exc, MantyxNetworkError):
        return True
    status = exc.status
    if status == 429:
        return True
    if status is not None and status >= 500:
        return True
    return False


def _quote(s: str) -> str:
    """Tight URL-path escape; keeps simple alphanumerics intact, percent-
    encodes anything else (so workspace slugs / ids round-trip safely)."""
    out: list[str] = []
    for ch in s:
        cp = ord(ch)
        if (
            (ord("a") <= cp <= ord("z"))
            or (ord("A") <= cp <= ord("Z"))
            or (ord("0") <= cp <= ord("9"))
            or ch in "-_."
        ):
            out.append(ch)
        else:
            for b in ch.encode("utf-8"):
                out.append(f"%{b:02X}")
    return "".join(out)


def _has_non_empty_system_message(messages: Sequence[ConversationMessage] | None) -> bool:
    if not messages:
        return False
    return any(
        str(m.get("role") or "") == "system" and str(m.get("content") or "").strip()
        for m in messages
        if isinstance(m, Mapping)
    )


def _serialize_turn_input(
    *,
    prompt: str | None = None,
    messages: Sequence[ConversationMessage] | None = None,
    attachments: Sequence[MessageAttachment] | None = None,
) -> dict[str, Any]:
    if messages is not None:
        return {"messages": list(messages)}
    if prompt is not None:
        if attachments:
            msg: ConversationMessage = {
                "role": "user",
                "content": prompt,
                "attachments": list(attachments),
            }
            return {"messages": [msg]}
        return {"prompt": prompt}
    return {}


def _serialize_agent_spec(
    *,
    agent_id: str | None,
    system_prompt: str | None,
    model_id: str | None,
    name: str | None,
    tools: list[ToolRef] | None,
    reasoning_level: ReasoningLevel | None,
    output_schema: OutputSchema | None,
    loop_detection: LoopDetection | bool | None = _UNSET,
    tool_budgets: ToolBudgets | None = _UNSET,
    supervisor: Supervisor | bool | None = _UNSET,
    plan: PlanSpec | None = _UNSET,
    budgets: RunBudgets | None,
    metadata: Metadata | None,
    prompt: str | None = None,
    messages: Sequence[ConversationMessage] | None = None,
    attachments: Sequence[MessageAttachment] | None = None,
) -> dict[str, Any]:
    identity_messages: Sequence[ConversationMessage] | None = messages
    if identity_messages is None and attachments and prompt is not None:
        identity_messages = [{"role": "user", "content": prompt}]
    if not agent_id and not system_prompt and not _has_non_empty_system_message(identity_messages):
        raise MantyxError(
            "Either agent_id, system_prompt, or a non-empty system message in messages is required"
        )
    body: dict[str, Any] = {"tools": serialize_tool_refs(tools)}
    if system_prompt:
        body["systemPrompt"] = system_prompt
    if agent_id:
        body["agentId"] = agent_id
    if name:
        body["name"] = name
    if model_id:
        body["modelId"] = model_id
    normalized_level = normalize_reasoning_level(reasoning_level)
    if normalized_level is not None:
        body["reasoningLevel"] = normalized_level
    normalized_schema = normalize_output_schema(output_schema)
    if normalized_schema is not None:
        body["outputSchema"] = normalized_schema
    if loop_detection is not _UNSET:
        normalized_loop = normalize_loop_detection(loop_detection)
        if normalized_loop is not None:
            body["loopDetection"] = normalized_loop
    if tool_budgets is not _UNSET:
        normalized_budgets = normalize_tool_budgets(tool_budgets)
        if normalized_budgets is not None:
            body["toolBudgets"] = normalized_budgets
    if supervisor is not _UNSET:
        normalized_supervisor = normalize_supervisor(supervisor)
        if normalized_supervisor is not None:
            body["supervisor"] = normalized_supervisor
    if plan is not _UNSET:
        normalized_plan = normalize_plan(plan)
        if normalized_plan is not None:
            body["plan"] = normalized_plan
    if budgets:
        body["budgets"] = dict(budgets)
    if metadata:
        body["metadata"] = dict(metadata)
    body.update(_serialize_turn_input(prompt=prompt, messages=messages, attachments=attachments))
    return body


def _describe_handler(ev: RunEvent, fallback: str) -> str:
    if str(ev.data.get("kind") or "") == "mcp_local":
        s = str(ev.data.get("mcpServer") or "")
        t = str(ev.data.get("mcpToolName") or "")
        if s and t:
            return f"{s}/{t}"
    return fallback


def _to_run_event(sse_ev: SseEvent, last_seq: int) -> RunEvent:
    data: dict[str, Any] = {}
    if sse_ev.data:
        try:
            parsed = json.loads(sse_ev.data)
            if isinstance(parsed, dict):
                data = parsed
        except json.JSONDecodeError:
            data = {}
    type_ = sse_ev.event or (data.get("type") if isinstance(data.get("type"), str) else "message")
    seq_raw = data.get("seq")
    seq = int(seq_raw) if isinstance(seq_raw, (int, float)) else last_seq
    payload = cast(dict[str, Any], data.get("data") if isinstance(data.get("data"), dict) else data)
    return RunEvent(seq=seq, type=str(type_), data=payload)


def _parse_model_catalog(body: Mapping[str, Any]) -> ModelCatalog:
    raw_models = body.get("models") if isinstance(body.get("models"), list) else []
    models: list[ModelInfo] = []
    for m in cast(Iterable[Any], raw_models):
        if not isinstance(m, dict):
            continue
        pricing = None
        p = m.get("pricing")
        if isinstance(p, dict):
            pricing = PricingInfo(
                inputPer1MUsd=_as_optional_float(p.get("inputPer1MUsd")),
                outputPer1MUsd=_as_optional_float(p.get("outputPer1MUsd")),
                cacheReadPer1MUsd=_as_optional_float(p.get("cacheReadPer1MUsd")),
            )
        ctx_raw = m.get("contextWindowTokens")
        ctx = int(ctx_raw) if isinstance(ctx_raw, (int, float)) else None
        models.append(
            ModelInfo(
                id=str(m.get("id") or ""),
                label=str(m.get("label") or ""),
                provider=str(m.get("provider") or ""),
                vendor_model_id=str(m.get("vendorModelId") or ""),
                source=str(m.get("source") or ""),
                context_window_tokens=ctx,
                pricing=pricing,
            )
        )
    default_raw = body.get("defaultModelId")
    return ModelCatalog(
        models=models,
        default_model_id=default_raw if isinstance(default_raw, str) else None,
    )


def _int_or_zero(v: Any) -> int:
    if isinstance(v, bool):
        return int(v)
    if isinstance(v, (int, float)):
        return int(v)
    return 0


def _coerce_str_map(raw: Any) -> dict[str, str]:
    out: dict[str, str] = {}
    if isinstance(raw, dict):
        for k, v in raw.items():
            out[str(k)] = str(v)
    return out


def _parse_session_summary(raw: Mapping[str, Any]) -> SessionSummary:
    return SessionSummary(
        session_id=str(raw.get("sessionId") or ""),
        creation_date=str(raw.get("creationDate") or ""),
        last_interaction_date=str(raw.get("lastInteractionDate") or ""),
        summary=str(raw.get("summary") or ""),
        metadata=_coerce_str_map(raw.get("metadata")),
        status=str(raw.get("status") or ""),
    )


def _parse_session_list(body: Mapping[str, Any]) -> SessionListResult:
    rows_raw = body.get("sessions") if isinstance(body.get("sessions"), list) else []
    sessions = [
        _parse_session_summary(r) for r in cast(Iterable[Any], rows_raw) if isinstance(r, dict)
    ]
    return SessionListResult(
        total=_int_or_zero(body.get("total")),
        limit=_int_or_zero(body.get("limit")),
        offset=_int_or_zero(body.get("offset")),
        sessions=sessions,
    )


def _parse_session_events(body: Mapping[str, Any]) -> list[RunEvent]:
    """Coerce the events endpoint's flattened frames (``{seq, type, ...}``)
    into :class:`RunEvent` instances, mirroring the live SSE shape."""
    events_raw = body.get("events") if isinstance(body.get("events"), list) else []
    events: list[RunEvent] = []
    for i, frame in enumerate(cast(Iterable[Any], events_raw)):
        if not isinstance(frame, dict):
            continue
        seq_raw = frame.get("seq")
        seq = int(seq_raw) if isinstance(seq_raw, (int, float)) else i + 1
        type_ = str(frame.get("type") or "message")
        data = {k: v for k, v in frame.items() if k not in ("seq", "type")}
        events.append(RunEvent(seq=seq, type=type_, data=data))
    return events


def _parse_session_info(body: Mapping[str, Any]) -> SessionInfo:
    msgs_raw = body.get("messages") if isinstance(body.get("messages"), list) else []
    messages: list[ConversationMessage] = []
    for m in cast(Iterable[Any], msgs_raw):
        if isinstance(m, dict):
            msg = cast(
                ConversationMessage,
                {
                    "role": str(m.get("role") or ""),
                    "content": str(m.get("content") or ""),
                },
            )
            attachments_raw = m.get("attachments")
            if isinstance(attachments_raw, list):
                msg["attachments"] = [
                    cast(AttachmentMetadata, a) for a in attachments_raw if isinstance(a, dict)
                ]
            messages.append(msg)
    metadata_raw = body.get("metadata")
    metadata: dict[str, str] = {}
    if isinstance(metadata_raw, dict):
        for k, v in metadata_raw.items():
            metadata[str(k)] = str(v)
    return SessionInfo(
        id=str(body.get("id") or ""),
        name=str(body.get("name") or ""),
        status=str(body.get("status") or ""),
        created_at=str(body.get("createdAt") or ""),
        last_used_at=str(body.get("lastUsedAt") or ""),
        ended_at=cast(str | None, body.get("endedAt")),
        agent_spec=cast(AgentSpec, body.get("agentSpec") or {}),
        messages=messages,
        metadata=metadata,
    )


def _parse_run_feedback(raw: Mapping[str, Any], run_id: str) -> RunFeedbackResult:
    verdict_raw = raw.get("verdict")
    if verdict_raw not in ("UP", "DOWN"):
        raise MantyxError(f"invalid feedback verdict in response: {verdict_raw!r}")
    return RunFeedbackResult(
        id=str(raw.get("id") or ""),
        verdict=cast(Literal["UP", "DOWN"], verdict_raw),
        target_kind=str(raw.get("targetKind") or ""),
        agent_run_id=str(raw.get("agentRunId") or run_id),
    )


def _coerce_eval_tools(
    agent: Mapping[str, Any] | None,
    tools: Sequence[ToolRef] | None,
) -> list[ToolRef] | None:
    merged: list[ToolRef] = []
    if agent is not None:
        merged.extend(_extract_tool_refs(agent.get("tools")))
    if tools:
        merged.extend(tools)
    return merged or None


def _extract_tool_refs(raw: Any) -> list[ToolRef]:
    if not isinstance(raw, list):
        return []
    out: list[ToolRef] = []
    for item in raw:
        if isinstance(item, dict):
            continue
        out.append(cast(ToolRef, item))
    return out


def _serialize_inline_eval_agent(
    agent: Mapping[str, Any],
    tools: list[ToolRef] | None,
) -> dict[str, Any]:
    wire = dict(agent)
    wire_tools: list[Any] = []
    raw_agent_tools = wire.pop("tools", None)
    if isinstance(raw_agent_tools, list):
        wire_tools.extend(t for t in raw_agent_tools if isinstance(t, dict))
        agent_tool_refs = _extract_tool_refs(raw_agent_tools)
        if agent_tool_refs:
            wire_tools.extend(serialize_tool_refs(agent_tool_refs))
    if tools:
        wire_tools.extend(serialize_tool_refs(tools))
    if wire_tools:
        wire["tools"] = wire_tools
    return wire


def _eval_event_to_run_event(ev: EvalRunEvent) -> RunEvent:
    return RunEvent(type=ev.type, data=cast(RunEventData, dict(ev.data)), seq=0)


def _serialize_create_eval_run(
    *,
    dataset_id: str | None,
    dataset: InlineEvalDatasetSpec | Mapping[str, Any] | None,
    agent_id: str | None,
    agent: Mapping[str, Any] | None,
    tools: list[ToolRef] | None,
    model_id: str | None,
    overrides: AgentEvalOverrides | Mapping[str, Any] | None,
) -> dict[str, Any]:
    has_dataset = dataset_id is not None or dataset is not None
    has_agent = agent_id is not None or agent is not None
    if not has_dataset:
        raise MantyxError("create_eval_run requires exactly one of dataset_id or dataset")
    if dataset_id is not None and dataset is not None:
        raise MantyxError("create_eval_run accepts only one of dataset_id or dataset")
    if not has_agent:
        raise MantyxError("create_eval_run requires exactly one of agent_id or agent")
    if agent_id is not None and agent is not None:
        raise MantyxError("create_eval_run accepts only one of agent_id or agent")
    body: dict[str, Any] = {}
    if dataset_id is not None:
        body["datasetId"] = dataset_id
    if dataset is not None:
        body["dataset"] = _inline_eval_dataset_to_wire(dataset)
    if agent_id is not None:
        body["agentId"] = agent_id
    if agent is not None:
        body["agent"] = _serialize_inline_eval_agent(agent, tools if agent_id is None else None)
    if tools is not None and agent_id is not None:
        body["tools"] = serialize_tool_refs(tools)
    if model_id is not None:
        body["modelId"] = model_id
    if overrides is not None:
        body["overrides"] = _agent_eval_overrides_to_wire(overrides)
    return body


def _inline_eval_dataset_to_wire(
    dataset: InlineEvalDatasetSpec | Mapping[str, Any],
) -> dict[str, Any]:
    if isinstance(dataset, InlineEvalDatasetSpec):
        wire: dict[str, Any] = {
            "cases": [_inline_eval_case_to_wire(c) for c in dataset.cases],
        }
        if dataset.name is not None:
            wire["name"] = dataset.name
        if dataset.tool_mocks is not None:
            wire["toolMocks"] = dataset.tool_mocks
        return wire
    return dict(dataset)


def _inline_eval_case_to_wire(case: InlineEvalCaseSpec | Mapping[str, Any]) -> dict[str, Any]:
    if isinstance(case, InlineEvalCaseSpec):
        wire: dict[str, Any] = {"input": dict(case.input)}
        if case.name is not None:
            wire["name"] = case.name
        if case.scorers is not None:
            wire["scorers"] = case.scorers
        if case.tool_mocks is not None:
            wire["toolMocks"] = case.tool_mocks
        if case.tags is not None:
            wire["tags"] = case.tags
        return wire
    return dict(case)


def _agent_eval_overrides_to_wire(
    overrides: AgentEvalOverrides | Mapping[str, Any],
) -> dict[str, Any]:
    if isinstance(overrides, AgentEvalOverrides):
        wire: dict[str, Any] = {}
        if overrides.system_prompt is not None:
            wire["systemPrompt"] = overrides.system_prompt
        if overrides.system_prompt_append is not None:
            wire["systemPromptAppend"] = overrides.system_prompt_append
        if overrides.model is not None:
            wire["model"] = overrides.model
        if overrides.llm_provider_id is not None:
            wire["llmProviderId"] = overrides.llm_provider_id
        if overrides.reasoning_level is not None:
            wire["reasoningLevel"] = overrides.reasoning_level
        if overrides.disable_tools is not None:
            wire["disableTools"] = overrides.disable_tools
        if overrides.tool_allowlist is not None:
            wire["toolAllowlist"] = overrides.tool_allowlist
        if overrides.disabled_mocks is not None:
            wire["disabledMocks"] = overrides.disabled_mocks
        return wire
    return dict(overrides)


def _parse_eval_dataset_summary(raw: Mapping[str, Any]) -> EvalDatasetSummary:
    return EvalDatasetSummary(
        id=str(raw.get("id") or ""),
        name=str(raw.get("name") or ""),
        description=raw.get("description") if isinstance(raw.get("description"), str) else None,
        case_count=int(raw.get("caseCount") or 0),
        run_count=int(raw.get("runCount") or 0),
        created_at=str(raw.get("createdAt") or ""),
        updated_at=str(raw.get("updatedAt") or ""),
    )


def _parse_eval_case(raw: Mapping[str, Any]) -> EvalCase:
    scorers_raw = raw.get("scorers")
    tags_raw = raw.get("tags")
    return EvalCase(
        id=str(raw.get("id") or ""),
        name=str(raw.get("name") or ""),
        input=cast(dict[str, Any], raw.get("input") if isinstance(raw.get("input"), dict) else {}),
        scorers=[s for s in scorers_raw if isinstance(s, dict)]
        if isinstance(scorers_raw, list)
        else [],
        tags=[str(t) for t in tags_raw if isinstance(t, str)] if isinstance(tags_raw, list) else [],
        tool_mocks=raw.get("toolMocks") if isinstance(raw.get("toolMocks"), dict) else None,
        created_at=str(raw.get("createdAt") or ""),
        updated_at=str(raw.get("updatedAt") or ""),
    )


def _parse_eval_dataset_list(body: Mapping[str, Any]) -> EvalDatasetList:
    datasets_raw = body.get("datasets")
    datasets = [
        _parse_eval_dataset_summary(d)
        for d in cast(Iterable[Any], datasets_raw or [])
        if isinstance(d, dict)
    ]
    return EvalDatasetList(datasets=datasets)


def _parse_eval_dataset_detail(body: Mapping[str, Any]) -> EvalDatasetDetail:
    cases_raw = body.get("cases")
    cases = [
        _parse_eval_case(c) for c in cast(Iterable[Any], cases_raw or []) if isinstance(c, dict)
    ]
    return EvalDatasetDetail(
        id=str(body.get("id") or ""),
        name=str(body.get("name") or ""),
        description=body.get("description") if isinstance(body.get("description"), str) else None,
        tool_mocks=body.get("toolMocks") if isinstance(body.get("toolMocks"), dict) else None,
        cases=cases,
        created_at=str(body.get("createdAt") or ""),
        updated_at=str(body.get("updatedAt") or ""),
    )


def _parse_eval_run_accepted(body: Mapping[str, Any]) -> EvalRunAccepted:
    return EvalRunAccepted(
        run_id=str(body.get("runId") or ""),
        status=str(body.get("status") or ""),
        stream_url=str(body.get("streamUrl") or ""),
    )


def _parse_eval_run_summary(raw: Mapping[str, Any]) -> EvalRunSummary:
    score_raw = raw.get("score")
    return EvalRunSummary(
        id=str(raw.get("id") or ""),
        dataset_id=str(raw.get("datasetId") or ""),
        dataset_name=str(raw.get("datasetName") or ""),
        agent_id=raw.get("agentId") if isinstance(raw.get("agentId"), str) else None,
        inline_agent=bool(raw.get("inlineAgent")),
        status=str(raw.get("status") or ""),
        total_cases=int(raw.get("totalCases") or 0),
        completed_cases=int(raw.get("completedCases") or 0),
        passed_cases=int(raw.get("passedCases") or 0),
        score=float(score_raw) if isinstance(score_raw, (int, float)) else None,
        token_usage=raw.get("tokenUsage") if isinstance(raw.get("tokenUsage"), dict) else None,
        error=raw.get("error") if isinstance(raw.get("error"), str) else None,
        agent_overrides=raw.get("agentOverrides")
        if isinstance(raw.get("agentOverrides"), dict)
        else None,
        started_at=raw.get("startedAt") if isinstance(raw.get("startedAt"), str) else None,
        finished_at=raw.get("finishedAt") if isinstance(raw.get("finishedAt"), str) else None,
        created_at=str(raw.get("createdAt") or ""),
    )


def _parse_eval_case_result(raw: Mapping[str, Any]) -> EvalCaseResult:
    case_raw = raw.get("case")
    case = _parse_eval_case(case_raw) if isinstance(case_raw, dict) else _parse_eval_case({})
    tool_calls_raw = raw.get("toolCalls")
    scores_raw = raw.get("scores")
    latency_raw = raw.get("latencyMs")
    latency_ms = int(latency_raw) if isinstance(latency_raw, (int, float)) else None
    return EvalCaseResult(
        id=str(raw.get("id") or ""),
        case_id=str(raw.get("caseId") or ""),
        case=case,
        final_text=raw.get("finalText") if isinstance(raw.get("finalText"), str) else None,
        tool_calls=[t for t in tool_calls_raw if isinstance(t, dict)]
        if isinstance(tool_calls_raw, list)
        else [],
        scores=[s for s in scores_raw if isinstance(s, dict)]
        if isinstance(scores_raw, list)
        else [],
        passed=bool(raw.get("passed")),
        score=float(raw.get("score") or 0),
        tokens=raw.get("tokens") if isinstance(raw.get("tokens"), dict) else None,
        latency_ms=latency_ms,
        error=raw.get("error") if isinstance(raw.get("error"), str) else None,
        created_at=str(raw.get("createdAt") or ""),
    )


def _parse_eval_run_detail(body: Mapping[str, Any]) -> EvalRunDetail:
    summary = _parse_eval_run_summary(body)
    results_raw = body.get("results")
    results = [
        _parse_eval_case_result(r)
        for r in cast(Iterable[Any], results_raw or [])
        if isinstance(r, dict)
    ]
    return EvalRunDetail(
        **summary.__dict__,
        inline_agent_spec=body.get("inlineAgentSpec")
        if isinstance(body.get("inlineAgentSpec"), dict)
        else None,
        agent_spec_snapshot=body.get("agentSpecSnapshot")
        if isinstance(body.get("agentSpecSnapshot"), dict)
        else None,
        updated_at=str(body.get("updatedAt") or ""),
        results=results,
    )


def _parse_eval_run_list(body: Mapping[str, Any]) -> EvalRunList:
    runs_raw = body.get("runs")
    runs = [
        _parse_eval_run_summary(r)
        for r in cast(Iterable[Any], runs_raw or [])
        if isinstance(r, dict)
    ]
    return EvalRunList(
        runs=runs,
        limit=int(body.get("limit") or 0),
        offset=int(body.get("offset") or 0),
    )


def _parse_eval_run_compare_side(raw: Mapping[str, Any]) -> EvalRunCompareSide:
    score_raw = raw.get("score")
    return EvalRunCompareSide(
        id=str(raw.get("id") or ""),
        agent_id=raw.get("agentId") if isinstance(raw.get("agentId"), str) else None,
        inline_agent=bool(raw.get("inlineAgent")),
        status=str(raw.get("status") or ""),
        total_cases=int(raw.get("totalCases") or 0),
        completed_cases=int(raw.get("completedCases") or 0),
        passed_cases=int(raw.get("passedCases") or 0),
        score=float(score_raw) if isinstance(score_raw, (int, float)) else None,
        token_usage=raw.get("tokenUsage") if isinstance(raw.get("tokenUsage"), dict) else None,
        agent_overrides=raw.get("agentOverrides")
        if isinstance(raw.get("agentOverrides"), dict)
        else None,
        agent_spec_snapshot=raw.get("agentSpecSnapshot")
        if isinstance(raw.get("agentSpecSnapshot"), dict)
        else None,
        started_at=raw.get("startedAt") if isinstance(raw.get("startedAt"), str) else None,
        finished_at=raw.get("finishedAt") if isinstance(raw.get("finishedAt"), str) else None,
        created_at=str(raw.get("createdAt") or ""),
    )


def _parse_eval_run_compare(body: Mapping[str, Any]) -> EvalRunCompare:
    cases_raw = body.get("cases")
    cases: list[EvalRunCompareCase] = []
    for c in cast(Iterable[Any], cases_raw or []):
        if not isinstance(c, dict):
            continue
        cases.append(
            EvalRunCompareCase(
                case_id=str(c.get("caseId") or ""),
                case_name=str(c.get("caseName") or ""),
                case_input=c.get("caseInput") if isinstance(c.get("caseInput"), dict) else None,
                a=c.get("a") if isinstance(c.get("a"), dict) else None,
                b=c.get("b") if isinstance(c.get("b"), dict) else None,
            )
        )
    run_a_raw = body.get("runA")
    run_b_raw = body.get("runB")
    return EvalRunCompare(
        dataset_id=str(body.get("datasetId") or ""),
        dataset_name=str(body.get("datasetName") or ""),
        run_a=_parse_eval_run_compare_side(run_a_raw if isinstance(run_a_raw, dict) else {}),
        run_b=_parse_eval_run_compare_side(run_b_raw if isinstance(run_b_raw, dict) else {}),
        cases=cases,
    )


def _parse_eval_run_event(raw: Any) -> EvalRunEvent:
    if not isinstance(raw, dict):
        return EvalRunEvent(type="message", data={})
    type_ = str(raw.get("type") or "message")
    data = {k: v for k, v in raw.items() if k != "type"}
    return EvalRunEvent(type=type_, data=data)


def _parse_run_tokens(raw: Any) -> RunTokenUsage | None:
    """Defensively coerce a wire ``tokens`` object into
    :class:`RunTokenUsage`.

    Returns ``None`` when the value is missing or not a dict — that
    keeps the "no usage data" sentinel intact against legacy MANTYX
    runners that omit the field entirely. Unknown / missing buckets
    default to ``0`` (the protocol contract is that misbehaving
    engines clamp to non-negative integers; the SDK mirrors that
    here so dashboards never see ``NaN`` or negatives).
    """
    if not isinstance(raw, dict):
        return None
    return RunTokenUsage(
        input_tokens=_to_non_negative_int(raw.get("inputTokens")),
        cached_tokens=_to_non_negative_int(raw.get("cachedTokens")),
        reasoning_tokens=_to_non_negative_int(raw.get("reasoningTokens")),
        output_tokens=_to_non_negative_int(raw.get("outputTokens")),
    )


def _parse_run_turns(raw: Any) -> int | None:
    """Coerce a wire ``turns`` value into a non-negative int.

    Returns ``None`` when the value is missing or unparseable so the
    caller can leave :attr:`RunResult.turns` at ``None`` against
    legacy servers.
    """
    if isinstance(raw, bool) or not isinstance(raw, (int, float)):
        return None
    return max(0, int(raw))


def _parse_run_model(raw: Any) -> RunModelInfo | None:
    """Defensively coerce a wire ``model`` object into
    :class:`RunModelInfo`. Returns ``None`` when the input isn't a
    dict — the "no usage data" sentinel for legacy servers.
    """
    if not isinstance(raw, dict):
        return None
    reasoning_raw = raw.get("reasoningEffort")
    reasoning = reasoning_raw if isinstance(reasoning_raw, str) and reasoning_raw else None
    return RunModelInfo(
        id=str(raw.get("id") or ""),
        provider=str(raw.get("provider") or ""),
        vendor_model_id=str(raw.get("vendorModelId") or ""),
        reasoning_effort=reasoning,
    )


def _to_non_negative_int(raw: Any) -> int:
    if isinstance(raw, bool) or not isinstance(raw, (int, float)):
        return 0
    if raw != raw:  # NaN
        return 0
    return max(0, int(raw))


def _as_optional_float(v: Any) -> float | None:
    if isinstance(v, (int, float)):
        return float(v)
    return None


def parse_run_output(
    result: RunResult,
    validator: Callable[[Any], Any] | None = None,
) -> Any:
    """Parse the terminal text of a :class:`RunResult` as JSON.

    When the run was submitted with ``output_schema``, MANTYX (via the LLM
    provider) guarantees the reply parses as JSON in the *vast* majority
    of cases. Transient model errors (refusal text, truncation under
    ``max_tokens`` pressure, exotic Unicode) can still produce strings
    that fail to ``json.loads`` in rare edge cases — this helper
    centralises that brittle step and surfaces a typed
    :class:`MantyxParseError` on failure with the original text preserved
    on ``err.text``.

    Pass an optional ``validator`` (a Pydantic ``model_validate``, an
    ``ajv.compile``-style validator, or any callable) to re-validate
    against your source-of-truth schema. Its return value is forwarded;
    any exception is wrapped in :class:`MantyxParseError`.

    Example::

        from pydantic import BaseModel
        from mantyx import parse_run_output

        class WeatherReport(BaseModel):
            city: str
            temperature_c: float

        result = client.run_agent(
            system_prompt="...",
            prompt="What's the weather in SF?",
            output_schema={"name": "weather_report", "schema": WEATHER_JSON_SCHEMA},
        )
        report = parse_run_output(result, WeatherReport.model_validate)
    """
    try:
        parsed = json.loads(result.text)
    except json.JSONDecodeError as exc:
        raise MantyxParseError(
            f"Run {result.run_id} returned non-JSON text; cannot satisfy output_schema",
            text=result.text,
            cause=exc,
        ) from exc
    if validator is None:
        return parsed
    try:
        return validator(parsed)
    except Exception as exc:
        raise MantyxParseError(
            f"Run {result.run_id} output failed validation: {exc}",
            text=result.text,
            cause=exc,
        ) from exc


__all__ = [
    "DEFAULT_BASE_URL",
    "AgentSession",
    "MantyxClient",
    "ModelCatalog",
    "ModelInfo",
    "PricingInfo",
    "RunEvent",
    "RunModelInfo",
    "RunResult",
    "RunTokenUsage",
    "SessionInfo",
    "SessionListResult",
    "SessionSummary",
    "parse_run_output",
]
