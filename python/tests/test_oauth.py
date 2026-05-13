"""Tests for the OAuth client + token-source plumbing.

Mirrors ``ts/test/oauth.test.ts`` across both sync and async clients.
"""

from __future__ import annotations

import asyncio
import re

import httpx
import pytest

from mantyx import (
    AsyncMantyxClient,
    MantyxAuthError,
    MantyxClient,
    MantyxError,
    MantyxOAuthClient,
    MantyxOAuthError,
    MantyxScopeError,
    generate_pkce_verifier,
    pkce_challenge,
)

from .conftest import MockServer

# ---------------------------------------------------------------------- PKCE


class TestPkceHelpers:
    def test_verifier_within_rfc_length_range(self) -> None:
        v = generate_pkce_verifier()
        assert 43 <= len(v) <= 128
        assert re.fullmatch(r"[A-Za-z0-9\-._~]+", v)

    def test_pkce_challenge_matches_rfc_test_vector(self) -> None:
        verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
        assert pkce_challenge(verifier) == "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"

    def test_rejects_out_of_range_length(self) -> None:
        with pytest.raises(MantyxError):
            generate_pkce_verifier(10)
        with pytest.raises(MantyxError):
            generate_pkce_verifier(200)


# --------------------------------------------------------------- helpers


def _oauth_client(server: MockServer) -> MantyxOAuthClient:
    transport = httpx.MockTransport(server.handle)
    http = httpx.Client(transport=transport, base_url="http://mock")
    return MantyxOAuthClient(
        client_id="mantyx_oa_test",
        client_secret="mantyx_oas_secret",
        base_url="http://mock",
        http_client=http,
    )


def _async_oauth_client(server: MockServer) -> MantyxOAuthClient:
    transport = httpx.MockTransport(server.handle)
    http = httpx.Client(transport=transport, base_url="http://mock")
    async_transport = httpx.MockTransport(server.handle)
    async_http = httpx.AsyncClient(transport=async_transport, base_url="http://mock")
    return MantyxOAuthClient(
        client_id="mantyx_oa_test",
        client_secret="mantyx_oas_secret",
        base_url="http://mock",
        http_client=http,
        async_http_client=async_http,
    )


# ---------------------------------------------------------------- exchange


class TestExchangeAuthorizationCode:
    def test_posts_form_body_and_returns_typed_token(self, mock_server: MockServer) -> None:
        oauth = _oauth_client(mock_server)
        token = oauth.exchange_authorization_code(
            code="auth_code_123",
            redirect_uri="https://app.example.com/cb",
            code_verifier="verifier_value",
        )
        assert token.access_token.startswith("mantyx_at_mock_initial_v")
        assert token.refresh_token == "mantyx_rt_mock_initial"
        assert token.token_type == "Bearer"
        assert token.expires_in == 3600
        assert token.expires_at > 0
        body = mock_server.oauth_last_token_request
        assert body is not None
        assert body["grant_type"] == "authorization_code"
        assert body["code"] == "auth_code_123"
        assert body["redirect_uri"] == "https://app.example.com/cb"
        assert body["code_verifier"] == "verifier_value"
        assert body["client_id"] == "mantyx_oa_test"
        assert body["client_secret"] == "mantyx_oas_secret"

    def test_invalid_grant_raises_oauth_error(self, mock_server: MockServer) -> None:
        oauth = _oauth_client(mock_server)
        mock_server.oauth_next_error = {"error": "invalid_grant", "description": "code expired"}
        with pytest.raises(MantyxOAuthError) as exc_info:
            oauth.exchange_authorization_code(
                code="bad", redirect_uri="https://app.example.com/cb", code_verifier="v"
            )
        assert exc_info.value.oauth_error == "invalid_grant"
        assert exc_info.value.oauth_error_description == "code expired"
        assert exc_info.value.status == 400


# ---------------------------------------------------------------- refresh


class TestRefresh:
    def test_returns_fresh_access_token_and_echoes_refresh(self, mock_server: MockServer) -> None:
        oauth = _oauth_client(mock_server)
        token = oauth.refresh(refresh_token="mantyx_rt_alice")
        assert token.access_token.startswith("mantyx_at_mock_initial_v")
        assert token.refresh_token == "mantyx_rt_alice"
        body = mock_server.oauth_last_token_request
        assert body is not None
        assert body["grant_type"] == "refresh_token"
        assert body["refresh_token"] == "mantyx_rt_alice"
        assert body["client_id"] == "mantyx_oa_test"

    def test_never_drifts_off_original_refresh_token(self, mock_server: MockServer) -> None:
        oauth = _oauth_client(mock_server)
        for _ in range(10):
            t = oauth.refresh(refresh_token="mantyx_rt_alice")
            assert t.refresh_token == "mantyx_rt_alice"
        assert mock_server.oauth_token_call_count == 10
        assert (mock_server.oauth_last_token_request or {}).get(
            "refresh_token"
        ) == "mantyx_rt_alice"

    def test_forwards_optional_scope_for_narrowing(self, mock_server: MockServer) -> None:
        oauth = _oauth_client(mock_server)
        oauth.refresh(refresh_token="mantyx_rt_alice", scope=["runs:write", "models:read"])
        body = mock_server.oauth_last_token_request or {}
        assert body.get("scope") == "runs:write models:read"

    def test_invalid_grant_surfaces_oauth_error(self, mock_server: MockServer) -> None:
        oauth = _oauth_client(mock_server)
        mock_server.oauth_next_error = {"error": "invalid_grant"}
        with pytest.raises(MantyxOAuthError) as exc_info:
            oauth.refresh(refresh_token="mantyx_rt_revoked")
        assert exc_info.value.oauth_error == "invalid_grant"


# ------------------------------------------------------- client_credentials


class TestClientCredentials:
    def test_posts_grant_type_and_returns_token_without_refresh(
        self, mock_server: MockServer
    ) -> None:
        oauth = _oauth_client(mock_server)
        token = oauth.client_credentials(scope="agents:invoke")
        assert token.access_token.startswith("mantyx_at_mock_initial")
        assert token.refresh_token is None
        body = mock_server.oauth_last_token_request or {}
        assert body.get("grant_type") == "client_credentials"
        assert body.get("scope") == "agents:invoke"


# --------------------------------------------------------------------- revoke


class TestRevoke:
    def test_posts_form_body_verbatim(self, mock_server: MockServer) -> None:
        oauth = _oauth_client(mock_server)
        oauth.revoke(token="mantyx_rt_to_kill")
        assert mock_server.oauth_revoke_call_count == 1
        body = mock_server.oauth_last_revoke_request or {}
        assert body["token"] == "mantyx_rt_to_kill"
        assert body["client_id"] == "mantyx_oa_test"
        assert body["client_secret"] == "mantyx_oas_secret"


# ---------------------------------------------- MantyxClient + token_source


def _client_with_source(
    mock_server: MockServer, oauth: MantyxOAuthClient, **source_kw
) -> MantyxClient:
    transport = httpx.MockTransport(mock_server.handle)
    http = httpx.Client(transport=transport, base_url="http://mock")
    return MantyxClient(
        token_source=oauth.refresh_token_source(**source_kw),
        workspace_slug="acme",
        base_url="http://mock",
        http_client=http,
    )


class TestRefreshTokenSourceSync:
    def test_mints_once_and_reuses_token(self, mock_server: MockServer) -> None:
        oauth = _oauth_client(mock_server)
        client = _client_with_source(mock_server, oauth, refresh_token="mantyx_rt_alice")
        client.list_models()
        client.list_models()
        assert mock_server.oauth_token_call_count == 1
        # Same bearer reused across requests.
        api_bearers = [h for h in mock_server.auth_header_history if "_mock_initial_v" in h]
        assert len(api_bearers) == 2
        assert api_bearers[0] == api_bearers[1]
        client.close()

    def test_proactive_refresh_with_huge_skew(self, mock_server: MockServer) -> None:
        oauth = _oauth_client(mock_server)
        client = _client_with_source(
            mock_server,
            oauth,
            refresh_token="mantyx_rt_alice",
            refresh_skew_s=1_000_000.0,
        )
        client.list_models()
        client.list_models()
        assert mock_server.oauth_token_call_count == 2
        client.close()

    def test_401_triggers_refresh_and_one_retry(self, mock_server: MockServer) -> None:
        oauth = _oauth_client(mock_server)
        client = _client_with_source(mock_server, oauth, refresh_token="mantyx_rt_alice")
        mock_server.fail_auth_count = 1
        catalog = client.list_models()
        assert catalog.default_model_id == "platform:demo"
        api_bearers = [h for h in mock_server.auth_header_history if "_mock_initial_v" in h]
        assert len(api_bearers) == 2
        assert api_bearers[0] != api_bearers[1]
        assert mock_server.oauth_token_call_count == 2
        client.close()

    def test_403_insufficient_scope_is_not_retried(self, mock_server: MockServer) -> None:
        oauth = _oauth_client(mock_server)
        client = _client_with_source(mock_server, oauth, refresh_token="mantyx_rt_alice")
        mock_server.fail_scope = ["runs:write"]
        with pytest.raises(MantyxScopeError):
            client.list_models()
        # Initial mint only — no extra refresh after the scope failure.
        assert mock_server.oauth_token_call_count == 1
        client.close()

    def test_second_401_throws_auth_error(self, mock_server: MockServer) -> None:
        oauth = _oauth_client(mock_server)
        client = _client_with_source(mock_server, oauth, refresh_token="mantyx_rt_alice")
        mock_server.fail_auth_count = 5
        with pytest.raises(MantyxAuthError):
            client.list_models()
        client.close()

    def test_single_flight_collapses_concurrent_refreshes(self, mock_server: MockServer) -> None:
        from concurrent.futures import ThreadPoolExecutor

        oauth = _oauth_client(mock_server)
        client = _client_with_source(
            mock_server,
            oauth,
            refresh_token="mantyx_rt_alice",
            refresh_skew_s=1_000_000.0,
        )
        mock_server.oauth_token_latency_s = 0.05
        with ThreadPoolExecutor(max_workers=8) as pool:
            list(pool.map(lambda _: client.list_models(), range(8)))
        assert mock_server.oauth_token_call_count == 1
        api_bearers = [h for h in mock_server.auth_header_history if "_mock_initial_v" in h]
        assert len(api_bearers) == 8
        client.close()

    def test_initial_token_seeds_cache(self, mock_server: MockServer) -> None:
        oauth = _oauth_client(mock_server)
        seed = oauth.exchange_authorization_code(
            code="auth_code", redirect_uri="https://app.example.com/cb", code_verifier="v"
        )
        baseline = mock_server.oauth_token_call_count
        assert seed.refresh_token is not None
        client = _client_with_source(
            mock_server,
            oauth,
            refresh_token=seed.refresh_token,
            initial_token=seed,
        )
        client.list_models()
        assert mock_server.oauth_token_call_count == baseline
        client.close()


# ---------------------------------------------- AsyncMantyxClient + source


def _async_client_with_source(
    mock_server: MockServer, oauth: MantyxOAuthClient, **source_kw
) -> AsyncMantyxClient:
    transport = httpx.MockTransport(mock_server.handle)
    http = httpx.AsyncClient(transport=transport, base_url="http://mock")
    return AsyncMantyxClient(
        token_source=oauth.async_refresh_token_source(**source_kw),
        workspace_slug="acme",
        base_url="http://mock",
        http_client=http,
    )


class TestRefreshTokenSourceAsync:
    @pytest.mark.asyncio
    async def test_mints_once_and_reuses_token(self, mock_server: MockServer) -> None:
        oauth = _async_oauth_client(mock_server)
        client = _async_client_with_source(mock_server, oauth, refresh_token="mantyx_rt_alice")
        await client.list_models()
        await client.list_models()
        assert mock_server.oauth_token_call_count == 1
        api_bearers = [h for h in mock_server.auth_header_history if "_mock_initial_v" in h]
        assert len(api_bearers) == 2
        assert api_bearers[0] == api_bearers[1]
        await client.aclose()
        await oauth.aclose()

    @pytest.mark.asyncio
    async def test_401_triggers_refresh_and_one_retry(self, mock_server: MockServer) -> None:
        oauth = _async_oauth_client(mock_server)
        client = _async_client_with_source(mock_server, oauth, refresh_token="mantyx_rt_alice")
        mock_server.fail_auth_count = 1
        catalog = await client.list_models()
        assert catalog.default_model_id == "platform:demo"
        api_bearers = [h for h in mock_server.auth_header_history if "_mock_initial_v" in h]
        assert len(api_bearers) == 2
        assert api_bearers[0] != api_bearers[1]
        await client.aclose()
        await oauth.aclose()

    @pytest.mark.asyncio
    async def test_single_flight_collapses_concurrent_refreshes(
        self, mock_server: MockServer
    ) -> None:
        # The mock transport runs synchronously and blocks the loop, so
        # we exercise single-flight by injecting an awaitable delay
        # directly into the mint function the source calls — this is
        # the only way to land multiple coroutines on the inflight
        # branch concurrently.
        oauth = _async_oauth_client(mock_server)
        gate = asyncio.Event()
        call_count = 0

        async def slow_mint() -> object:
            nonlocal call_count
            call_count += 1
            await gate.wait()
            return await oauth.arefresh(refresh_token="mantyx_rt_alice")

        from mantyx.oauth import _AsyncTokenCache

        cache = _AsyncTokenCache(initial=None, skew_s=1_000_000.0)
        source = cache.as_source(slow_mint)

        transport = httpx.MockTransport(mock_server.handle)
        http = httpx.AsyncClient(transport=transport, base_url="http://mock")
        client = AsyncMantyxClient(
            token_source=source,
            workspace_slug="acme",
            base_url="http://mock",
            http_client=http,
        )
        tasks = [asyncio.create_task(client.list_models()) for _ in range(8)]
        # Yield so the tasks run up to the gate.wait() inside slow_mint.
        await asyncio.sleep(0)
        await asyncio.sleep(0)
        gate.set()
        await asyncio.gather(*tasks)
        # Exactly one mint was issued despite 8 concurrent callers.
        assert call_count == 1
        assert mock_server.oauth_token_call_count == 1
        await client.aclose()
        await oauth.aclose()

    @pytest.mark.asyncio
    async def test_403_insufficient_scope_is_not_retried(self, mock_server: MockServer) -> None:
        oauth = _async_oauth_client(mock_server)
        client = _async_client_with_source(mock_server, oauth, refresh_token="mantyx_rt_alice")
        mock_server.fail_scope = ["runs:write"]
        with pytest.raises(MantyxScopeError):
            await client.list_models()
        assert mock_server.oauth_token_call_count == 1
        await client.aclose()
        await oauth.aclose()


# ---------------------------------------------- credential validation


class TestCredentialValidation:
    def test_accepts_token_source_as_sole_credential(self, mock_server: MockServer) -> None:
        oauth = _oauth_client(mock_server)
        transport = httpx.MockTransport(mock_server.handle)
        http = httpx.Client(transport=transport, base_url="http://mock")
        client = MantyxClient(
            token_source=oauth.refresh_token_source(refresh_token="mantyx_rt_alice"),
            workspace_slug="acme",
            base_url="http://mock",
            http_client=http,
        )
        client.close()

    def test_rejects_api_key_plus_token_source(self, mock_server: MockServer) -> None:
        oauth = _oauth_client(mock_server)
        with pytest.raises(MantyxError):
            MantyxClient(
                api_key="mantyx_key",
                token_source=oauth.refresh_token_source(refresh_token="mantyx_rt_alice"),
                workspace_slug="acme",
                base_url="http://mock",
            )

    def test_rejects_all_three(self, mock_server: MockServer) -> None:
        oauth = _oauth_client(mock_server)
        with pytest.raises(MantyxError):
            MantyxClient(
                api_key="mantyx_key",
                access_token="mantyx_at_test",
                token_source=oauth.refresh_token_source(refresh_token="mantyx_rt_alice"),
                workspace_slug="acme",
                base_url="http://mock",
            )
