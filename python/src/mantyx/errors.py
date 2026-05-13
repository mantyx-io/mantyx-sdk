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
    """The server rejected the API key / OAuth access token (HTTP 401)."""

    def __init__(self, message: str = "Invalid or missing API key / OAuth access token") -> None:
        super().__init__(message, code="unauthorized", status=401)


class MantyxOAuthError(MantyxError):
    """Raised on a non-2xx from ``/api/oauth/token`` or ``/api/oauth/revoke``.

    Carries the RFC 6749 ``error`` discriminator (``"invalid_grant"``,
    ``"invalid_client"``, ``"unsupported_grant_type"``, …) on
    :attr:`oauth_error` and the optional ``error_description`` on
    :attr:`oauth_error_description` so callers can branch without
    parsing the human-readable message.

    ``invalid_grant`` from the refresh path specifically signals the
    refresh token has been revoked (or its grant / application was
    deleted); the SDK does **not** loop on this — callers should route
    the user back to a fresh sign-in.
    """

    def __init__(
        self,
        oauth_error: str,
        oauth_error_description: str | None,
        status: int,
    ) -> None:
        message = (
            f"OAuth {oauth_error}: {oauth_error_description}"
            if oauth_error_description
            else f"OAuth {oauth_error}"
        )
        super().__init__(message, code=oauth_error, status=status)
        self.oauth_error = oauth_error
        self.oauth_error_description = oauth_error_description


class MantyxScopeError(MantyxError):
    """Raised on ``403 insufficient_scope`` from the server.

    This signals that the OAuth 2.0 access token presented on the
    request is missing one of the scopes the route demands (see
    ``docs/agent-runs-protocol.md`` §2.2 for the per-endpoint table).

    The ``required_scopes`` attribute carries the verbatim ``required``
    value from the server's response — a single scope for most routes,
    an array when the route demands more than one. Surface this to the
    caller so they can drive a re-consent flow (e.g. "please re-authorise
    the app with ``sessions:write`` enabled").

    Workspace API keys never trip this error — they carry no granular
    scopes. It is OAuth-only.
    """

    def __init__(
        self,
        message: str,
        required_scopes: list[str] | tuple[str, ...] | None = None,
    ) -> None:
        super().__init__(message, code="insufficient_scope", status=403)
        self.required_scopes: tuple[str, ...] = tuple(required_scopes or ())


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
    "MantyxOAuthError",
    "MantyxParseError",
    "MantyxRunError",
    "MantyxScopeError",
    "MantyxToolError",
]
