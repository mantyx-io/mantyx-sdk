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
    """The agent loop terminated with a non-success ``result`` event."""

    def __init__(self, run_id: str, subtype: str, message: str) -> None:
        super().__init__(message, code=subtype)
        self.run_id = run_id
        self.subtype = subtype


__all__ = [
    "MantyxAuthError",
    "MantyxError",
    "MantyxNetworkError",
    "MantyxRunError",
    "MantyxToolError",
]
