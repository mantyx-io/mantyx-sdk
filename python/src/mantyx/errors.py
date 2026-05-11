"""Error types raised by the MANTYX SDK."""

from __future__ import annotations


class MantyxError(Exception):
    """Base class for every error raised by the MANTYX SDK."""

    def __init__(
        self,
        message: str,
        *,
        code: str = "mantyx_error",
        status: int | None = None,
        hint: str | None = None,
    ) -> None:
        super().__init__(message)
        self.message = message
        self.code = code
        self.status = status
        self.hint = hint

    def __repr__(self) -> str:
        bits = [f"{type(self).__name__}({self.message!r}", f"code={self.code!r}"]
        if self.status is not None:
            bits.append(f"status={self.status}")
        return ", ".join(bits) + ")"


class MantyxNetworkError(MantyxError):
    """Transport-layer failure (DNS, TCP reset, timeout)."""

    def __init__(self, message: str, *, cause: BaseException | None = None) -> None:
        super().__init__(message, code="network")
        if cause is not None:
            self.__cause__ = cause


class MantyxAuthError(MantyxError):
    """The server rejected the API key (401) or the workspace mismatch (403)."""

    def __init__(self, message: str = "Invalid or missing API key") -> None:
        super().__init__(message, code="unauthorized", status=401)


class MantyxToolError(MantyxError):
    """A local tool handler raised or timed out."""

    def __init__(self, tool_name: str, message: str) -> None:
        super().__init__(
            f"Local tool {tool_name!r} failed: {message}",
            code="local_tool_failed",
        )
        self.tool_name = tool_name


class MantyxRunError(MantyxError):
    """The agent loop terminated with a non-success ``result`` event,
    a terminal ``error`` event, or was cancelled by the caller / server.

    When the run ended via a terminal ``error`` event (e.g. the model
    truncated mid-reply), the optional triage attributes carry the
    structured fields documented in
    `docs/agent-runs-protocol.md` §7 so callers can render UI banners
    ("truncated reply — JSON likely incomplete") and drive retry policy
    without re-parsing the human-readable ``message``:

    - ``error_class`` — canonical category (``"rate_limit"``,
      ``"overloaded"``, ``"server"``, ``"context_window"``,
      ``"truncation"``, ``"invalid_request"``, ``"auth"``, ``"timeout"``,
      ``"local_timeout"``, ``"upstream_deadline"``, ``"unknown"``). New
      categories may land additively.
    - ``finish_reason`` — canonical lowercase provider stop reason
      (``"max_tokens"``, ``"refusal"``, ``"malformed_function_call"``,
      …). Mirrors the last ``assistant_message`` event's
      ``finishReason``.
    - ``partial_text`` — **best-effort raw bytes** the model emitted
      before the failure. For ``output_schema`` runs this is likely
      **incomplete JSON** that will fail ``json.loads`` — treat it as
      diagnostic data, never as a schema-conformant reply.
    - ``retryable`` — coarse retry hint from the pipeline's classifier.
    """

    def __init__(
        self,
        run_id: str,
        subtype: str,
        message: str,
        *,
        error_class: str | None = None,
        finish_reason: str | None = None,
        partial_text: str | None = None,
        retryable: bool | None = None,
    ) -> None:
        super().__init__(message, code=subtype)
        self.run_id = run_id
        self.subtype = subtype
        self.error_class = error_class
        self.finish_reason = finish_reason
        self.partial_text = partial_text
        self.retryable = retryable


class MantyxParseError(MantyxError):
    """Raised by :func:`mantyx.parse_run_output` when the run's terminal text
    cannot be JSON-parsed (or fails the user-supplied validator).

    The original assistant text is preserved on the ``text`` attribute so
    callers can log the raw model output for debugging.
    """

    def __init__(
        self,
        message: str,
        *,
        text: str,
        cause: BaseException | None = None,
    ) -> None:
        super().__init__(message, code="output_parse_failed")
        self.text = text
        if cause is not None:
            self.__cause__ = cause


__all__ = [
    "MantyxAuthError",
    "MantyxError",
    "MantyxNetworkError",
    "MantyxParseError",
    "MantyxRunError",
    "MantyxToolError",
]
