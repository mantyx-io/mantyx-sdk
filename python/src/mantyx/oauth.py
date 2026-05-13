"""MANTYX OAuth 2.0 client: authorization-code exchange, refresh-token
minting, client-credentials grant, and token revocation, plus
:class:`TokenSource` adapters that :class:`MantyxClient` /
:class:`AsyncMantyxClient` consume to refresh access tokens
transparently before they expire (and again on 401).

The wire contract this implements is ``docs/oauth.md`` in the SDK
monorepo:

- Token endpoint: ``POST <base_url>/api/oauth/token``, form-encoded.
- Revoke endpoint: ``POST <base_url>/api/oauth/revoke``, form-encoded.
- Access tokens (``mantyx_at_…``) live 1 hour (``expires_in: 3600``).
- Refresh tokens (``mantyx_rt_…``) are **persistent and non-rotating**:
  ``grant_type=refresh_token`` echoes back the same value the client
  sent. The caller persists the refresh token once at first sign-in
  (encrypted at rest) and the SDK re-mints access tokens from it on
  demand.

The SDK does **not** drive the browser/native redirect dance — the
calling app owns that — but ships ``generate_pkce_verifier`` and
``pkce_challenge`` helpers so the consent step can stay self-contained,
plus :meth:`MantyxOAuthClient.exchange_authorization_code` to swap a
returned ``code`` for the initial ``{access_token, refresh_token}``
pair.
"""

from __future__ import annotations

import asyncio
import base64
import hashlib
import secrets
import string
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

    :attr:`refresh_token` is populated on the initial
    ``authorization_code`` exchange and on subsequent ``refresh_token``
    calls (where it is identical to the value the client just sent —
    refresh tokens never rotate). The ``client_credentials`` grant
    never returns one.
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
    :meth:`MantyxOAuthClient.refresh_token_source` or
    :meth:`MantyxOAuthClient.client_credentials_token_source` are
    thread-safe and single-flight expired-token observers into one
    token-endpoint call.
    """

    def __call__(self, reason: TokenRequestReason = "initial") -> str: ...


@runtime_checkable
class AsyncTokenSource(Protocol):
    """Async equivalent of :class:`TokenSource`.

    Returns an awaitable yielding the current access token. Used by
    :class:`AsyncMantyxClient`.
    """

    def __call__(self, reason: TokenRequestReason = "initial") -> Awaitable[str]: ...


# --------------------------------------------------------------- PKCE helpers


_PKCE_ALPHABET = string.ascii_letters + string.digits + "-._~"


def generate_pkce_verifier(length: int = 64) -> str:
    """Return a high-entropy PKCE ``code_verifier`` (RFC 7636 §4.1).

    The verifier is the raw secret the caller keeps across the
    redirect; the ``code_challenge`` you send on
    ``/api/oauth/authorize`` is derived from it via
    :func:`pkce_challenge`. Length must satisfy ``43 <= length <= 128``
    per the RFC.
    """
    if not 43 <= length <= 128:
        raise MantyxError("PKCE code_verifier length must be in [43, 128]")
    return "".join(secrets.choice(_PKCE_ALPHABET) for _ in range(length))


def pkce_challenge(verifier: str) -> str:
    """Compute ``base64url(sha256(verifier))`` with no padding (RFC 7636 §4.2)."""
    digest = hashlib.sha256(verifier.encode("utf-8")).digest()
    return base64.urlsafe_b64encode(digest).rstrip(b"=").decode("ascii")


# ----------------------------------------------------------------- OAuth client


class MantyxOAuthClient:
    """Wraps the MANTYX OAuth 2.0 authorization-server endpoints.

    App-scoped (one per ``(client_id, client_secret)`` pair); construct
    independently of :class:`MantyxClient`, then either call its grant
    helpers directly or hand a token source it produces to
    ``MantyxClient(token_source=...)`` for transparent refresh.

    The same instance powers both the sync (:meth:`refresh_token_source`)
    and async (:meth:`async_refresh_token_source`) flavours; pick the
    builder that matches the client you're feeding.
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

    def exchange_authorization_code(
        self, *, code: str, redirect_uri: str, code_verifier: str
    ) -> OAuthToken:
        """Swap an authorization code + PKCE verifier for the initial
        ``{access_token, refresh_token}`` pair.

        Call this exactly once per sign-in after the browser / native
        redirect lands back on your ``redirect_uri`` with a ``code``
        query parameter. Persist the returned ``refresh_token`` — it
        is long-lived and non-rotating per ``docs/oauth.md``.
        """
        return self._token(
            {
                "grant_type": "authorization_code",
                "code": code,
                "redirect_uri": redirect_uri,
                "code_verifier": code_verifier,
            }
        )

    def refresh(
        self, *, refresh_token: str, scope: str | Sequence[str] | None = None
    ) -> OAuthToken:
        """Mint a fresh access token from a stored refresh token.

        The returned ``refresh_token`` is identical to the input — the
        field is surfaced for symmetry with
        :meth:`exchange_authorization_code` only.

        Raises :class:`MantyxOAuthError` with ``oauth_error="invalid_grant"``
        if the refresh has been revoked (or its grant / app was
        deleted); the caller must drive a fresh sign-in.
        """
        if not refresh_token:
            raise MantyxError("`refresh_token` is required for MantyxOAuthClient.refresh")
        body = {"grant_type": "refresh_token", "refresh_token": refresh_token}
        scope_str = _normalize_scope(scope)
        if scope_str is not None:
            body["scope"] = scope_str
        return self._token(body)

    def client_credentials(self, *, scope: str | Sequence[str] | None = None) -> OAuthToken:
        """Request a workspace-scoped access token without a user.

        Available only on private OAuth apps registered with
        ``allowsClientCredentials: true``. No refresh token is issued;
        re-call this method whenever a new access token is needed.
        """
        body = {"grant_type": "client_credentials"}
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

    def client_credentials_token_source(
        self,
        *,
        scope: str | Sequence[str] | None = None,
        refresh_skew_s: float = DEFAULT_REFRESH_SKEW_S,
    ) -> TokenSource:
        """Build a sync :class:`TokenSource` backed by the
        ``client_credentials`` grant.

        On every refresh the source re-mints a workspace-scoped access
        token by calling the token endpoint with
        ``grant_type=client_credentials``. Available only on private
        apps with ``allowsClientCredentials: true``.
        """
        cache = _SyncTokenCache(None, refresh_skew_s)

        def mint() -> OAuthToken:
            return self.client_credentials(scope=scope)

        return cache.as_source(mint)

    def async_client_credentials_token_source(
        self,
        *,
        scope: str | Sequence[str] | None = None,
        refresh_skew_s: float = DEFAULT_REFRESH_SKEW_S,
    ) -> AsyncTokenSource:
        """Async variant of :meth:`client_credentials_token_source`."""
        cache = _AsyncTokenCache(None, refresh_skew_s)

        async def mint() -> OAuthToken:
            return await self.aclient_credentials(scope=scope)

        return cache.as_source(mint)

    # -------------------------------------------------------------- Async grants

    async def aexchange_authorization_code(
        self, *, code: str, redirect_uri: str, code_verifier: str
    ) -> OAuthToken:
        """Async :meth:`exchange_authorization_code`."""
        return await self._token_async(
            {
                "grant_type": "authorization_code",
                "code": code,
                "redirect_uri": redirect_uri,
                "code_verifier": code_verifier,
            }
        )

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

    async def aclient_credentials(self, *, scope: str | Sequence[str] | None = None) -> OAuthToken:
        """Async :meth:`client_credentials`."""
        body = {"grant_type": "client_credentials"}
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
            # Wait for the leader, outside the lock.
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
            # Coroutine-only synchronisation; no thread lock needed
            # because asyncio guarantees only one coroutine runs at a
            # time on the loop.
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
    "generate_pkce_verifier",
    "pkce_challenge",
]
