"""MANTYX OAuth 2.0 refresh client.

Trade a stored refresh token for short-lived access tokens, revoke
tokens at sign-out, and expose :class:`TokenSource` /
:class:`AsyncTokenSource` adapters that :class:`MantyxClient` /
:class:`AsyncMantyxClient` consume to refresh access tokens
transparently before they expire (and again on 401).

The library is intentionally **refresh-only**. It assumes the caller
already obtained the refresh token through their own sign-in flow
(Authorization Code + PKCE in a browser, native redirect, server-side
exchange — whatever fits the host application). The SDK does not
drive consent, does not initiate auth-code exchanges, and does not
bundle PKCE helpers.

Wire contract (``docs/oauth.md``):

- Token endpoint: ``POST <base_url>/api/oauth/token``, form-encoded,
  ``grant_type=refresh_token``. Echoes back the same ``refresh_token``
  the client sent (refresh tokens are persistent and non-rotating).
- Revoke endpoint: ``POST <base_url>/api/oauth/revoke``, form-encoded.
- Access tokens (``mantyx_at_…``) live 1 hour.
- Refresh tokens (``mantyx_rt_…``) are long-lived; the caller persists
  them once at first sign-in (encrypted at rest) and the SDK re-mints
  access tokens from the same value on demand.
"""

from __future__ import annotations

import asyncio
import threading
import time
from collections.abc import Awaitable, Callable, Sequence
from concurrent.futures import Future
from dataclasses import dataclass
from typing import Any, Literal, Protocol, runtime_checkable

import httpx

from ._version import SDK_VERSION
from .errors import MantyxError, MantyxNetworkError, MantyxOAuthError

DEFAULT_OAUTH_BASE_URL = "https://app.mantyx.io"
"""Origin of the MANTYX deployment; OAuth endpoints live at ``<base>/api/oauth/...``."""

DEFAULT_REFRESH_SKEW_S = 60.0
"""Seconds before ``expires_at`` at which a TokenSource pre-emptively refreshes."""

TokenRequestReason = Literal["initial", "expired", "unauthorized"]
"""Why the SDK asked the :class:`TokenSource` for the current access token."""


@dataclass(frozen=True)
class OAuthToken:
    """Decoded ``POST /api/oauth/token`` response.

    On the ``refresh_token`` grant :attr:`refresh_token` is identical
    to the value the client just sent — refresh tokens never rotate.
    The field is surfaced for symmetry with whatever the calling
    app's sign-in flow already does.
    """

    access_token: str
    refresh_token: str | None
    token_type: str
    expires_in: int
    expires_at: float
    """Absolute Unix-seconds timestamp set when the SDK parsed the response."""
    scope: str | None


@runtime_checkable
class TokenSource(Protocol):
    """Sync callable returning the current access token on demand.

    Implementations are called before every request and again with
    ``reason="unauthorized"`` after a 401 (forcing a refresh of the
    cached value). Concrete sources built via
    :meth:`MantyxOAuthClient.refresh_token_source` are thread-safe
    and single-flight expired-token observers into one token-endpoint
    call.
    """

    def __call__(self, reason: TokenRequestReason = "initial") -> str: ...


@runtime_checkable
class AsyncTokenSource(Protocol):
    """Async equivalent of :class:`TokenSource`.

    Returns an awaitable yielding the current access token. Used by
    :class:`AsyncMantyxClient`.
    """

    def __call__(self, reason: TokenRequestReason = "initial") -> Awaitable[str]: ...


# ----------------------------------------------------------------- OAuth client


class MantyxOAuthClient:
    """Refresh-only wrapper around the MANTYX OAuth 2.0 endpoints.

    App-scoped (one per ``(client_id, client_secret)`` pair). Construct
    independently of :class:`MantyxClient`, then either call
    :meth:`refresh` / :meth:`revoke` directly or hand a token source
    produced by :meth:`refresh_token_source` /
    :meth:`async_refresh_token_source` to a client constructor for
    transparent refresh.

    The client deliberately does **not** drive the authorization-code
    exchange or any other "initiate sign-in" grant. The caller is
    expected to obtain the refresh token through their own consent
    flow and persist it before constructing this client.
    """

    def __init__(
        self,
        *,
        client_id: str,
        client_secret: str,
        base_url: str = DEFAULT_OAUTH_BASE_URL,
        timeout: float = 30.0,
        http_client: httpx.Client | None = None,
        async_http_client: httpx.AsyncClient | None = None,
    ) -> None:
        """Construct the OAuth client.

        :param client_id: OAuth ``client_id`` issued at app registration
            (token prefix ``mantyx_oa_``).
        :param client_secret: OAuth ``client_secret`` issued at app
            registration (token prefix ``mantyx_oas_``). Every MANTYX
            OAuth app is a confidential client, so this is always
            required for token + revoke calls. Treat as a deployment
            secret — do not bundle into browser builds.
        :param base_url: Origin of the MANTYX deployment. Defaults to
            ``https://app.mantyx.io``.
        :param timeout: Per-request timeout in seconds. Default: 30s.
        :param http_client: Optional shared :class:`httpx.Client`.
            Defaults to one this object owns.
        :param async_http_client: Optional shared :class:`httpx.AsyncClient`.
            Lazily created on first async use otherwise.
        """
        if not client_id:
            raise MantyxError("`client_id` is required for MantyxOAuthClient")
        if not client_secret:
            raise MantyxError("`client_secret` is required for MantyxOAuthClient")
        self.client_id = client_id
        self._client_secret = client_secret
        self.base_url = base_url.rstrip("/")
        self.timeout = timeout
        self._owns_sync_http = http_client is None
        self._sync_http = http_client or httpx.Client(
            timeout=httpx.Timeout(timeout),
            headers={"User-Agent": f"mantyx-sdk-python/{SDK_VERSION}"},
        )
        self._owns_async_http = async_http_client is None
        self._async_http = async_http_client  # lazily instantiated

    # ------------------------------------------------------------------ ctx

    def __enter__(self) -> MantyxOAuthClient:
        return self

    def __exit__(self, *exc: Any) -> None:
        self.close()

    def close(self) -> None:
        """Close any HTTP clients this object owns."""
        if self._owns_sync_http:
            self._sync_http.close()

    async def aclose(self) -> None:
        """Close any HTTP clients this object owns (async)."""
        if self._owns_async_http and self._async_http is not None:
            await self._async_http.aclose()

    def _get_async_http(self) -> httpx.AsyncClient:
        if self._async_http is None:
            self._async_http = httpx.AsyncClient(
                timeout=httpx.Timeout(self.timeout),
                headers={"User-Agent": f"mantyx-sdk-python/{SDK_VERSION}"},
            )
        return self._async_http

    # --------------------------------------------------------------- Sync grants

    def refresh(
        self, *, refresh_token: str, scope: str | Sequence[str] | None = None
    ) -> OAuthToken:
        """Mint a fresh access token from a stored refresh token.

        The returned ``refresh_token`` is identical to the input —
        refresh tokens are persistent and non-rotating, so the field
        is surfaced only for symmetry with the response shape.

        Raises :class:`MantyxOAuthError` with
        ``oauth_error="invalid_grant"`` if the refresh has been
        revoked (or its grant / app was deleted); the caller must
        drive a fresh sign-in.
        """
        if not refresh_token:
            raise MantyxError("`refresh_token` is required for MantyxOAuthClient.refresh")
        body = {"grant_type": "refresh_token", "refresh_token": refresh_token}
        scope_str = _normalize_scope(scope)
        if scope_str is not None:
            body["scope"] = scope_str
        return self._token(body)

    def revoke(self, *, token: str) -> None:
        """Revoke an access or refresh token (RFC 7009).

        The server always returns 200, even for unknown tokens.
        Revoking the **refresh** token kills the refresh and every
        live access token tied to its grant; revoking an **access**
        token kills only that one.
        """
        if not token:
            raise MantyxError("`token` is required for MantyxOAuthClient.revoke")
        self._form_post_sync("/api/oauth/revoke", {"token": token})

    def refresh_token_source(
        self,
        *,
        refresh_token: str,
        scope: str | Sequence[str] | None = None,
        refresh_skew_s: float = DEFAULT_REFRESH_SKEW_S,
        initial_token: OAuthToken | None = None,
    ) -> TokenSource:
        """Build a long-lived sync :class:`TokenSource` that re-mints
        access tokens from the supplied refresh token.

        Pass the returned source to
        ``MantyxClient(token_source=..., workspace_slug=...)``. The
        source caches the access token in-memory and refreshes
        proactively when within ``refresh_skew_s`` of ``expires_at``,
        or eagerly when :class:`MantyxClient` reports a 401.

        Pass ``initial_token`` if the calling app already has a
        non-expired access token in hand (e.g. straight out of the
        sign-in flow) to avoid an extra round-trip on the first call.
        """
        if not refresh_token:
            raise MantyxError(
                "`refresh_token` is required for MantyxOAuthClient.refresh_token_source"
            )
        cache = _SyncTokenCache(initial_token, refresh_skew_s)

        def mint() -> OAuthToken:
            return self.refresh(refresh_token=refresh_token, scope=scope)

        return cache.as_source(mint)

    def async_refresh_token_source(
        self,
        *,
        refresh_token: str,
        scope: str | Sequence[str] | None = None,
        refresh_skew_s: float = DEFAULT_REFRESH_SKEW_S,
        initial_token: OAuthToken | None = None,
    ) -> AsyncTokenSource:
        """Async variant of :meth:`refresh_token_source` for use with
        :class:`AsyncMantyxClient`."""
        if not refresh_token:
            raise MantyxError(
                "`refresh_token` is required for MantyxOAuthClient.async_refresh_token_source"
            )
        cache = _AsyncTokenCache(initial_token, refresh_skew_s)

        async def mint() -> OAuthToken:
            return await self.arefresh(refresh_token=refresh_token, scope=scope)

        return cache.as_source(mint)

    # -------------------------------------------------------------- Async grants

    async def arefresh(
        self, *, refresh_token: str, scope: str | Sequence[str] | None = None
    ) -> OAuthToken:
        """Async :meth:`refresh`."""
        if not refresh_token:
            raise MantyxError("`refresh_token` is required for MantyxOAuthClient.arefresh")
        body = {"grant_type": "refresh_token", "refresh_token": refresh_token}
        scope_str = _normalize_scope(scope)
        if scope_str is not None:
            body["scope"] = scope_str
        return await self._token_async(body)

    async def arevoke(self, *, token: str) -> None:
        """Async :meth:`revoke`."""
        if not token:
            raise MantyxError("`token` is required for MantyxOAuthClient.arevoke")
        await self._form_post_async("/api/oauth/revoke", {"token": token})

    # ---------------------------------------------------------------- internals

    def _token(self, body: dict[str, str]) -> OAuthToken:
        resp = self._form_post_sync("/api/oauth/token", body)
        return self._parse_token_response(resp)

    async def _token_async(self, body: dict[str, str]) -> OAuthToken:
        resp = await self._form_post_async("/api/oauth/token", body)
        return self._parse_token_response(resp)

    def _parse_token_response(self, resp: httpx.Response) -> OAuthToken:
        try:
            parsed: dict[str, Any] = resp.json()
        except Exception as exc:
            raise MantyxOAuthError(
                "invalid_response",
                "Token endpoint returned a non-JSON response",
                resp.status_code,
            ) from exc
        access_token = parsed.get("access_token")
        if not isinstance(access_token, str) or not access_token:
            raise MantyxOAuthError(
                "invalid_response",
                "Token endpoint response is missing `access_token`",
                resp.status_code,
            )
        expires_in_raw = parsed.get("expires_in")
        expires_in = int(expires_in_raw) if isinstance(expires_in_raw, (int, float)) else 3600
        refresh = parsed.get("refresh_token")
        scope = parsed.get("scope")
        token_type = parsed.get("token_type") or "Bearer"
        return OAuthToken(
            access_token=access_token,
            refresh_token=refresh if isinstance(refresh, str) else None,
            token_type=str(token_type),
            expires_in=expires_in,
            expires_at=time.time() + expires_in,
            scope=scope if isinstance(scope, str) else None,
        )

    def _form_post_sync(self, path: str, body: dict[str, str]) -> httpx.Response:
        url = f"{self.base_url}{path}"
        data = {**body, "client_id": self.client_id, "client_secret": self._client_secret}
        try:
            resp = self._sync_http.post(
                url,
                data=data,
                headers={
                    "Content-Type": "application/x-www-form-urlencoded",
                    "Accept": "application/json",
                },
            )
        except httpx.HTTPError as exc:
            raise MantyxNetworkError(f"OAuth network error: {exc}", cause=exc) from exc
        _raise_for_oauth_status(resp)
        return resp

    async def _form_post_async(self, path: str, body: dict[str, str]) -> httpx.Response:
        url = f"{self.base_url}{path}"
        data = {**body, "client_id": self.client_id, "client_secret": self._client_secret}
        http = self._get_async_http()
        try:
            resp = await http.post(
                url,
                data=data,
                headers={
                    "Content-Type": "application/x-www-form-urlencoded",
                    "Accept": "application/json",
                },
            )
        except httpx.HTTPError as exc:
            raise MantyxNetworkError(f"OAuth network error: {exc}", cause=exc) from exc
        _raise_for_oauth_status(resp)
        return resp


# ----------------------------------------------------------- internal cache


class _SyncTokenCache:
    """Thread-safe single-flight cache for sync token sources.

    Concurrent callers that observe an expiring token coalesce onto a
    single in-flight :class:`Future` so only one ``mint()`` runs at a
    time — every other thread blocks on the future and returns the
    same result. This is an efficiency, not correctness, optimisation
    (per ``docs/oauth.md`` the server allows concurrent refreshes),
    but it keeps token-endpoint QPS reasonable under fan-out.
    """

    def __init__(self, initial: OAuthToken | None, skew_s: float) -> None:
        self._token: OAuthToken | None = initial
        self._skew_s = skew_s
        self._lock = threading.Lock()
        self._inflight: Future[OAuthToken] | None = None

    def as_source(self, mint: Callable[[], OAuthToken]) -> TokenSource:
        def source(reason: TokenRequestReason = "initial") -> str:
            if reason != "unauthorized":
                cached = self._token
                if cached is not None and not _is_expiring(cached, self._skew_s):
                    return cached.access_token
            with self._lock:
                if reason != "unauthorized":
                    cached = self._token
                    if cached is not None and not _is_expiring(cached, self._skew_s):
                        return cached.access_token
                if self._inflight is not None:
                    follower_future = self._inflight
                    is_leader = False
                else:
                    follower_future = Future()
                    self._inflight = follower_future
                    is_leader = True
            if is_leader:
                try:
                    fresh = mint()
                except BaseException as exc:
                    follower_future.set_exception(exc)
                    with self._lock:
                        self._inflight = None
                    raise
                self._token = fresh
                follower_future.set_result(fresh)
                with self._lock:
                    self._inflight = None
                return fresh.access_token
            return follower_future.result().access_token

        return source


class _AsyncTokenCache:
    """Single-flight cache for async token sources.

    Mirrors :class:`_SyncTokenCache`, using an :class:`asyncio.Future`
    so concurrent coroutines coalesce onto one mint. The asyncio
    primitives are lazily created on first use so the source can be
    constructed outside any event-loop context.
    """

    def __init__(self, initial: OAuthToken | None, skew_s: float) -> None:
        self._token: OAuthToken | None = initial
        self._skew_s = skew_s
        self._inflight: asyncio.Future[OAuthToken] | None = None

    def as_source(self, mint: Callable[[], Awaitable[OAuthToken]]) -> AsyncTokenSource:
        async def source(reason: TokenRequestReason = "initial") -> str:
            if reason != "unauthorized":
                cached = self._token
                if cached is not None and not _is_expiring(cached, self._skew_s):
                    return cached.access_token
            if self._inflight is not None:
                fresh = await self._inflight
                return fresh.access_token
            if reason != "unauthorized":
                cached = self._token
                if cached is not None and not _is_expiring(cached, self._skew_s):
                    return cached.access_token
            loop = asyncio.get_running_loop()
            inflight: asyncio.Future[OAuthToken] = loop.create_future()
            self._inflight = inflight
            try:
                fresh = await mint()
            except BaseException as exc:
                inflight.set_exception(exc)
                self._inflight = None
                raise
            self._token = fresh
            inflight.set_result(fresh)
            self._inflight = None
            return fresh.access_token

        return source


# ----------------------------------------------------------------- utilities


def _is_expiring(token: OAuthToken, skew_s: float) -> bool:
    return token.expires_at - time.time() <= skew_s


def _normalize_scope(scope: str | Sequence[str] | None) -> str | None:
    if scope is None:
        return None
    if isinstance(scope, str):
        trimmed = scope.strip()
        return trimmed if trimmed else None
    joined = " ".join(s for s in scope if isinstance(s, str) and s)
    return joined or None


def _raise_for_oauth_status(resp: httpx.Response) -> None:
    if resp.status_code < 400:
        return
    body: dict[str, Any] = {}
    try:
        body = resp.json() or {}
    except Exception:
        pass
    oauth_error = body.get("error")
    description = body.get("error_description")
    raise MantyxOAuthError(
        str(oauth_error) if isinstance(oauth_error, str) else f"http_{resp.status_code}",
        description if isinstance(description, str) else None,
        resp.status_code,
    )


__all__ = [
    "DEFAULT_OAUTH_BASE_URL",
    "DEFAULT_REFRESH_SKEW_S",
    "AsyncTokenSource",
    "MantyxOAuthClient",
    "OAuthToken",
    "TokenRequestReason",
    "TokenSource",
]
