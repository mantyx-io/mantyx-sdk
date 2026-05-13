/**
 * MANTYX OAuth 2.0 refresh client: trade a stored refresh token for
 * short-lived access tokens, revoke tokens at sign-out, and expose
 * a {@link TokenSource} the {@link MantyxClient} HTTP layer calls
 * before every request (and again on 401).
 *
 * The library is intentionally **refresh-only**. It assumes the caller
 * already obtained the refresh token through their own sign-in flow
 * (Authorization Code + PKCE in a browser, native redirect, server-
 * side exchange — whatever fits the host application). The SDK does
 * not drive consent, does not initiate auth-code exchanges, and does
 * not bundle PKCE helpers.
 *
 * Wire contract (`docs/oauth.md`):
 *
 * - Token endpoint: `POST <baseUrl>/api/oauth/token`, form-encoded,
 *   `grant_type=refresh_token`. Echoes back the same `refresh_token`
 *   the client sent (refresh tokens are persistent and non-rotating).
 * - Revoke endpoint: `POST <baseUrl>/api/oauth/revoke`, form-encoded.
 * - Access tokens (`mantyx_at_…`) live 1 hour (`expires_in: 3600`).
 * - Refresh tokens (`mantyx_rt_…`) are long-lived; the caller persists
 *   them once at first sign-in (encrypted at rest) and the SDK re-mints
 *   access tokens from the same value on demand.
 */

import { MantyxError, MantyxNetworkError } from "./errors.js";

export const DEFAULT_OAUTH_BASE_URL = "https://app.mantyx.io";

/** Skew (ms) before `expiresAt` at which a TokenSource will pre-emptively refresh. Default 60s. */
export const DEFAULT_REFRESH_SKEW_MS = 60_000;

/**
 * Raised on a non-2xx response from `POST /api/oauth/token` or
 * `POST /api/oauth/revoke`. Carries the RFC 6749 `error` discriminator
 * (`"invalid_grant"`, `"invalid_client"`, `"unsupported_grant_type"`,
 * …) and the optional `error_description` so callers can branch on
 * machine-readable values without parsing the human message.
 *
 * `invalid_grant` from the refresh path specifically signals that the
 * refresh token has been revoked (or the OAuth grant / application
 * was deleted). The SDK never loops on this — callers should route
 * the user back to a fresh sign-in.
 */
export class MantyxOAuthError extends MantyxError {
  readonly oauthError: string;
  readonly oauthErrorDescription: string | undefined;

  constructor(
    oauthError: string,
    oauthErrorDescription: string | undefined,
    status: number,
  ) {
    const message = oauthErrorDescription
      ? `OAuth ${oauthError}: ${oauthErrorDescription}`
      : `OAuth ${oauthError}`;
    super(message, { code: oauthError, status });
    this.name = "MantyxOAuthError";
    this.oauthError = oauthError;
    this.oauthErrorDescription = oauthErrorDescription;
  }
}

/**
 * Decoded `POST /api/oauth/token` response, augmented with an absolute
 * `expiresAt` timestamp the SDK uses to decide when to refresh.
 *
 * On the refresh grant the response's `refreshToken` is identical to
 * the value the client just sent (refresh tokens never rotate). The
 * field is surfaced for symmetry with whatever the calling app's
 * sign-in flow already does.
 */
export interface OAuthToken {
  readonly accessToken: string;
  readonly refreshToken: string | undefined;
  readonly tokenType: string;
  readonly expiresIn: number;
  /** Absolute Unix-ms timestamp set when the SDK parsed the response. */
  readonly expiresAt: number;
  readonly scope: string | undefined;
}

/** Why the SDK asked the {@link TokenSource} for the current access token. */
export type TokenRequestReason = "initial" | "expired" | "unauthorized";

/**
 * A `TokenSource` produces the current access token on demand. The
 * {@link MantyxClient} HTTP layer calls it before every request. When
 * called with `reason: "unauthorized"` the source MUST force a refresh
 * (do not return a cached value); this is how the SDK recovers from
 * 401s caused by a token that the server already invalidated.
 *
 * Implementations should be safe to call from many concurrent requests.
 */
export type TokenSource = (reason?: TokenRequestReason) => Promise<string>;

/** Caller-supplied options for `MantyxOAuthClient`. */
export interface MantyxOAuthClientOptions {
  /**
   * OAuth `client_id` issued at app registration (token prefix
   * `mantyx_oa_`).
   */
  clientId: string;
  /**
   * OAuth `client_secret` issued at app registration (token prefix
   * `mantyx_oas_`). Every MANTYX OAuth app is a confidential client,
   * so this is always required for token + revoke calls. Treat as a
   * deployment secret — do not bundle into browser builds.
   */
  clientSecret: string;
  /**
   * Origin of the MANTYX deployment. Defaults to `https://app.mantyx.io`.
   * The OAuth endpoints are mounted at `<baseUrl>/api/oauth/...`.
   */
  baseUrl?: string;
  /** Optional `fetch` override (e.g. node-fetch wrapper). Default: global `fetch`. */
  fetch?: typeof fetch;
  /** Default per-request timeout in milliseconds. Default: 30s. */
  timeoutMs?: number;
}

export interface RefreshOptions {
  refreshToken: string;
  /**
   * Optional scope narrowing. Must be a subset of the scopes already
   * granted to the refresh token (server enforces this). Useful when
   * an SDK consumer wants a short-scope access token for a specific
   * sub-operation.
   */
  scope?: string | readonly string[];
}

export interface RevokeOptions {
  token: string;
}

export interface RefreshTokenSourceOptions {
  refreshToken: string;
  /** Optional scope narrowing applied on every refresh. */
  scope?: string | readonly string[];
  /**
   * How many ms before `expiresAt` the source proactively refreshes.
   * Defaults to {@link DEFAULT_REFRESH_SKEW_MS} (60s).
   */
  refreshSkewMs?: number;
  /**
   * Optional initial access token + expiry to seed the source's cache
   * with (e.g. the token already in hand from the host application's
   * sign-in flow). When omitted, the source mints one on the first
   * call.
   */
  initialToken?: OAuthToken;
}

/**
 * Refresh-only wrapper around the MANTYX OAuth 2.0 authorization-server
 * endpoints. App-scoped (one per `{clientId, clientSecret}` pair);
 * construct independently of {@link MantyxClient}, then either call
 * {@link refresh} / {@link revoke} directly or hand a `TokenSource`
 * produced by {@link refreshTokenSource} to `MantyxClient` for fully
 * transparent refresh on every request.
 *
 * The client deliberately does **not** drive the authorization-code
 * exchange or any other "initiate sign-in" grant. The caller is
 * expected to obtain the refresh token through their own consent flow
 * and persist it before constructing this client.
 */
export class MantyxOAuthClient {
  readonly clientId: string;
  readonly baseUrl: string;
  private readonly clientSecret: string;
  private readonly fetchImpl: typeof fetch;
  private readonly timeoutMs: number;

  constructor(opts: MantyxOAuthClientOptions) {
    if (!opts.clientId) {
      throw new MantyxError("`clientId` is required for MantyxOAuthClient");
    }
    if (!opts.clientSecret) {
      throw new MantyxError("`clientSecret` is required for MantyxOAuthClient");
    }
    const f = opts.fetch ?? globalThis.fetch;
    if (typeof f !== "function") {
      throw new MantyxError(
        "Global fetch is not available; pass a custom `fetch` implementation in MantyxOAuthClientOptions.",
      );
    }
    this.clientId = opts.clientId;
    this.clientSecret = opts.clientSecret;
    this.baseUrl = (opts.baseUrl ?? DEFAULT_OAUTH_BASE_URL).replace(/\/+$/, "");
    this.fetchImpl = f;
    this.timeoutMs = opts.timeoutMs ?? 30_000;
  }

  /**
   * Mint a fresh access token from a stored refresh token. The
   * returned `refreshToken` is identical to the input — refresh
   * tokens are persistent and non-rotating, so the field is
   * surfaced only for symmetry with the response shape.
   *
   * On `400 invalid_grant` the refresh token has been revoked (or its
   * grant / app was deleted); the SDK surfaces a
   * {@link MantyxOAuthError} and callers must drive a fresh sign-in.
   */
  async refresh(opts: RefreshOptions): Promise<OAuthToken> {
    if (!opts.refreshToken) {
      throw new MantyxError("`refreshToken` is required for MantyxOAuthClient.refresh");
    }
    const body: Record<string, string> = {
      grant_type: "refresh_token",
      refresh_token: opts.refreshToken,
    };
    const scope = normalizeScope(opts.scope);
    if (scope !== undefined) body.scope = scope;
    return this.token(body);
  }

  /**
   * Revoke an access or refresh token (RFC 7009). The server always
   * returns 200, even for unknown tokens. Revoking a **refresh**
   * token kills the refresh and every live access token tied to its
   * grant; revoking an **access** token kills only that one.
   */
  async revoke(opts: RevokeOptions): Promise<void> {
    if (!opts.token) {
      throw new MantyxError("`token` is required for MantyxOAuthClient.revoke");
    }
    await this.formPost("/api/oauth/revoke", {
      token: opts.token,
    });
  }

  /**
   * Build a long-lived {@link TokenSource} that re-mints access
   * tokens from the supplied refresh token. Pass the returned source
   * to `new MantyxClient({ tokenSource, workspaceSlug, ... })`. The
   * source caches the access token in-memory and refreshes
   * proactively when the cached value is within `refreshSkewMs` of
   * `expiresAt`, or eagerly when `MantyxClient` reports a 401.
   *
   * Pass `initialToken` if the calling app already has a non-expired
   * access token in hand (e.g. straight out of the sign-in flow) to
   * avoid an extra round-trip on the first request.
   */
  refreshTokenSource(opts: RefreshTokenSourceOptions): TokenSource {
    if (!opts.refreshToken) {
      throw new MantyxError("`refreshToken` is required for MantyxOAuthClient.refreshTokenSource");
    }
    const skew = opts.refreshSkewMs ?? DEFAULT_REFRESH_SKEW_MS;
    const cache: TokenCache = { token: opts.initialToken, inflight: null };
    const refreshToken = opts.refreshToken;
    return makeTokenSource(cache, skew, async () => {
      return this.refresh({ refreshToken, scope: opts.scope });
    });
  }

  // -------------------------------------------------------------- internals

  /**
   * POST `application/x-www-form-urlencoded` to `/api/oauth/token` and
   * decode the {@link OAuthToken} response. Always injects `client_id`
   * + `client_secret` from the constructor.
   */
  private async token(body: Record<string, string>): Promise<OAuthToken> {
    const res = await this.formPost("/api/oauth/token", body);
    let parsed: Record<string, unknown> = {};
    try {
      parsed = (await res.json()) as Record<string, unknown>;
    } catch {
      throw new MantyxOAuthError(
        "invalid_response",
        "Token endpoint returned a non-JSON response",
        res.status,
      );
    }
    const accessToken = typeof parsed.access_token === "string" ? parsed.access_token : "";
    if (!accessToken) {
      throw new MantyxOAuthError(
        "invalid_response",
        "Token endpoint response is missing `access_token`",
        res.status,
      );
    }
    const expiresIn = typeof parsed.expires_in === "number" ? parsed.expires_in : 3600;
    return {
      accessToken,
      refreshToken: typeof parsed.refresh_token === "string" ? parsed.refresh_token : undefined,
      tokenType: typeof parsed.token_type === "string" ? parsed.token_type : "Bearer",
      expiresIn,
      expiresAt: Date.now() + expiresIn * 1000,
      scope: typeof parsed.scope === "string" ? parsed.scope : undefined,
    };
  }

  private async formPost(path: string, body: Record<string, string>): Promise<Response> {
    const url = `${this.baseUrl}${path}`;
    const params = new URLSearchParams({
      ...body,
      client_id: this.clientId,
      client_secret: this.clientSecret,
    });
    const ctrl = new AbortController();
    const t = setTimeout(() => ctrl.abort(), this.timeoutMs);
    let res: Response;
    try {
      res = await this.fetchImpl(url, {
        method: "POST",
        headers: {
          "Content-Type": "application/x-www-form-urlencoded",
          Accept: "application/json",
        },
        body: params.toString(),
        signal: ctrl.signal,
      });
    } catch (err) {
      if (ctrl.signal.aborted) {
        throw new MantyxNetworkError(`OAuth request timed out after ${this.timeoutMs}ms`);
      }
      throw new MantyxNetworkError(`OAuth network error: ${(err as Error).message}`, {
        cause: err,
      });
    } finally {
      clearTimeout(t);
    }
    if (!res.ok) {
      let errBody: { error?: unknown; error_description?: unknown } = {};
      try {
        errBody = (await res.json()) as typeof errBody;
      } catch {
        // ignore
      }
      const oauthError = typeof errBody.error === "string" ? errBody.error : `http_${res.status}`;
      const desc =
        typeof errBody.error_description === "string" ? errBody.error_description : undefined;
      throw new MantyxOAuthError(oauthError, desc, res.status);
    }
    return res;
  }
}

// -------------------------------------------------------------- internals

interface TokenCache {
  token: OAuthToken | undefined;
  inflight: Promise<OAuthToken> | null;
}

/**
 * Wrap a `mintToken` thunk into a single-flight {@link TokenSource}
 * with a cache + proactive-refresh skew. The cache is overwritten
 * atomically on every successful mint; the in-flight promise
 * collapses N concurrent expired-token observers into one mint call.
 *
 * Single-flight is an efficiency, not a correctness requirement —
 * `docs/oauth.md` explicitly allows multiple concurrent refreshes
 * against the same refresh token — but it keeps the token-endpoint
 * QPS reasonable when an SDK consumer fans out work in parallel.
 */
function makeTokenSource(
  cache: TokenCache,
  skewMs: number,
  mint: () => Promise<OAuthToken>,
): TokenSource {
  return async (reason: TokenRequestReason = "initial"): Promise<string> => {
    if (reason !== "unauthorized" && cache.token && !isExpiring(cache.token, skewMs)) {
      return cache.token.accessToken;
    }
    if (cache.inflight) {
      const t = await cache.inflight;
      if (reason === "unauthorized" && t === cache.token) {
        // If the inflight refresh was triggered by a benign cache miss
        // and we observed an unauthorized hint after it started, fall
        // through and mint again so the caller never gets a stale token.
      } else {
        return t.accessToken;
      }
    }
    cache.inflight = mint().then(
      (t) => {
        cache.token = t;
        return t;
      },
      (err: unknown) => {
        throw err;
      },
    );
    try {
      const t = await cache.inflight;
      return t.accessToken;
    } finally {
      cache.inflight = null;
    }
  };
}

function isExpiring(token: OAuthToken, skewMs: number): boolean {
  return token.expiresAt - Date.now() <= skewMs;
}

function normalizeScope(scope: string | readonly string[] | undefined): string | undefined {
  if (scope === undefined) return undefined;
  if (typeof scope === "string") {
    const trimmed = scope.trim();
    return trimmed.length > 0 ? trimmed : undefined;
  }
  const joined = scope.filter((s) => typeof s === "string" && s.length > 0).join(" ");
  return joined.length > 0 ? joined : undefined;
}
