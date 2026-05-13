import { afterEach, beforeEach, describe, expect, it } from "vitest";
import {
  MantyxAuthError,
  MantyxClient,
  MantyxOAuthClient,
  MantyxScopeError,
} from "../src/index.js";
import { MockServer } from "./helpers/mock-server.js";

describe("MantyxOAuthClient.refresh", () => {
  let server: MockServer;
  let oauth: MantyxOAuthClient;
  beforeEach(async () => {
    server = new MockServer();
    await server.start();
    oauth = new MantyxOAuthClient({
      clientId: "mantyx_oa_test",
      clientSecret: "mantyx_oas_secret",
      baseUrl: server.baseUrl(),
    });
  });
  afterEach(async () => {
    await server.stop();
  });

  it("returns a fresh access token and echoes the input refresh token", async () => {
    const token = await oauth.refresh({ refreshToken: "mantyx_rt_alice" });
    expect(token.accessToken).toMatch(/^mantyx_at_mock_initial_v\d+$/);
    expect(token.refreshToken).toBe("mantyx_rt_alice");
    expect(server.oauth.lastTokenRequest).toMatchObject({
      grant_type: "refresh_token",
      refresh_token: "mantyx_rt_alice",
      client_id: "mantyx_oa_test",
      client_secret: "mantyx_oas_secret",
    });
  });

  it("never drifts off the original refresh token across many calls", async () => {
    for (let i = 0; i < 10; i++) {
      const t = await oauth.refresh({ refreshToken: "mantyx_rt_alice" });
      expect(t.refreshToken).toBe("mantyx_rt_alice");
    }
    expect(server.oauth.tokenCallCount).toBe(10);
    expect(server.oauth.lastTokenRequest?.refresh_token).toBe("mantyx_rt_alice");
  });

  it("forwards an optional `scope` for subset narrowing", async () => {
    await oauth.refresh({
      refreshToken: "mantyx_rt_alice",
      scope: ["runs:write", "models:read"],
    });
    expect(server.oauth.lastTokenRequest?.scope).toBe("runs:write models:read");
  });

  it("surfaces invalid_grant when the refresh token has been revoked", async () => {
    server.oauth.nextError = { error: "invalid_grant", description: "refresh revoked" };
    await expect(oauth.refresh({ refreshToken: "mantyx_rt_revoked" })).rejects.toMatchObject({
      name: "MantyxOAuthError",
      oauthError: "invalid_grant",
      oauthErrorDescription: "refresh revoked",
      status: 400,
    });
  });
});

describe("MantyxOAuthClient.revoke", () => {
  let server: MockServer;
  beforeEach(async () => {
    server = new MockServer();
    await server.start();
  });
  afterEach(async () => {
    await server.stop();
  });

  it("posts the form body verbatim", async () => {
    const oauth = new MantyxOAuthClient({
      clientId: "mantyx_oa_test",
      clientSecret: "mantyx_oas_secret",
      baseUrl: server.baseUrl(),
    });
    await oauth.revoke({ token: "mantyx_rt_to_kill" });
    expect(server.oauth.revokeCallCount).toBe(1);
    expect(server.oauth.lastRevokeRequest).toMatchObject({
      token: "mantyx_rt_to_kill",
      client_id: "mantyx_oa_test",
      client_secret: "mantyx_oas_secret",
    });
  });
});

describe("MantyxClient + refreshTokenSource", () => {
  let server: MockServer;
  let oauth: MantyxOAuthClient;
  beforeEach(async () => {
    server = new MockServer();
    await server.start();
    oauth = new MantyxOAuthClient({
      clientId: "mantyx_oa_test",
      clientSecret: "mantyx_oas_secret",
      baseUrl: server.baseUrl(),
    });
  });
  afterEach(async () => {
    await server.stop();
  });

  it("mints an access token on the first call and reuses it for subsequent ones", async () => {
    const tokenSource = oauth.refreshTokenSource({ refreshToken: "mantyx_rt_alice" });
    const client = new MantyxClient({
      tokenSource,
      workspaceSlug: "demo",
      baseUrl: server.baseUrl(),
    });
    await client.listModels();
    await client.listModels();
    expect(server.oauth.tokenCallCount).toBe(1);
    expect(server.authHeaderHistory.every((h) => h.startsWith("Bearer mantyx_at_mock_initial_v"))).toBe(
      true,
    );
  });

  it("refreshes proactively when within the skew window", async () => {
    const tokenSource = oauth.refreshTokenSource({
      refreshToken: "mantyx_rt_alice",
      refreshSkewMs: 1_000_000_000, // huge skew → every call counts as expiring
    });
    const client = new MantyxClient({
      tokenSource,
      workspaceSlug: "demo",
      baseUrl: server.baseUrl(),
    });
    await client.listModels();
    await client.listModels();
    expect(server.oauth.tokenCallCount).toBe(2);
  });

  it("refreshes once and retries the original request after a 401", async () => {
    const tokenSource = oauth.refreshTokenSource({ refreshToken: "mantyx_rt_alice" });
    const client = new MantyxClient({
      tokenSource,
      workspaceSlug: "demo",
      baseUrl: server.baseUrl(),
    });
    server.failAuthCount = 1;
    const catalog = await client.listModels();
    expect(catalog.defaultModelId).toBe("platform:demo");
    expect(server.authHeaderHistory.length).toBe(2);
    expect(server.oauth.tokenCallCount).toBe(2);
    expect(server.authHeaderHistory[0]).not.toBe(server.authHeaderHistory[1]);
  });

  it("does not retry on 403 insufficient_scope; surfaces MantyxScopeError", async () => {
    const tokenSource = oauth.refreshTokenSource({ refreshToken: "mantyx_rt_alice" });
    const client = new MantyxClient({
      tokenSource,
      workspaceSlug: "demo",
      baseUrl: server.baseUrl(),
    });
    server.failScope = { required: ["runs:write"] };
    await expect(client.listModels()).rejects.toBeInstanceOf(MantyxScopeError);
    expect(server.oauth.tokenCallCount).toBe(1);
  });

  it("throws MantyxAuthError if the retry also 401s", async () => {
    const tokenSource = oauth.refreshTokenSource({ refreshToken: "mantyx_rt_alice" });
    const client = new MantyxClient({
      tokenSource,
      workspaceSlug: "demo",
      baseUrl: server.baseUrl(),
    });
    server.failAuthCount = 5;
    await expect(client.listModels()).rejects.toBeInstanceOf(MantyxAuthError);
  });

  it("single-flights concurrent expired-token observers into one /token call", async () => {
    const tokenSource = oauth.refreshTokenSource({
      refreshToken: "mantyx_rt_alice",
      refreshSkewMs: 1_000_000_000,
    });
    const client = new MantyxClient({
      tokenSource,
      workspaceSlug: "demo",
      baseUrl: server.baseUrl(),
    });
    server.oauth.tokenLatencyMs = 50;
    await Promise.all(Array.from({ length: 8 }, () => client.listModels()));
    expect(server.oauth.tokenCallCount).toBe(1);
    expect(server.authHeaderHistory).toHaveLength(8);
  });

  it("seeds the cache with `initialToken` and skips the first /token call", async () => {
    const seed = await oauth.refresh({ refreshToken: "mantyx_rt_alice" });
    const tokenCallsAfterSeed = server.oauth.tokenCallCount;
    const tokenSource = oauth.refreshTokenSource({
      refreshToken: seed.refreshToken!,
      initialToken: seed,
    });
    const client = new MantyxClient({
      tokenSource,
      workspaceSlug: "demo",
      baseUrl: server.baseUrl(),
    });
    await client.listModels();
    // No extra token call beyond the seed-time one.
    expect(server.oauth.tokenCallCount).toBe(tokenCallsAfterSeed);
  });
});

describe("MantyxClient credential validation", () => {
  let server: MockServer;
  beforeEach(async () => {
    server = new MockServer();
    await server.start();
  });
  afterEach(async () => {
    await server.stop();
  });

  it("accepts a tokenSource as the sole credential", () => {
    const oauth = new MantyxOAuthClient({
      clientId: "mantyx_oa_test",
      clientSecret: "mantyx_oas_secret",
      baseUrl: server.baseUrl(),
    });
    expect(
      () =>
        new MantyxClient({
          tokenSource: oauth.refreshTokenSource({ refreshToken: "mantyx_rt_alice" }),
          workspaceSlug: "demo",
          baseUrl: server.baseUrl(),
        }),
    ).not.toThrow();
  });

  it("rejects passing both apiKey and tokenSource", () => {
    const oauth = new MantyxOAuthClient({
      clientId: "mantyx_oa_test",
      clientSecret: "mantyx_oas_secret",
      baseUrl: server.baseUrl(),
    });
    expect(
      () =>
        new MantyxClient({
          apiKey: "mantyx_key",
          tokenSource: oauth.refreshTokenSource({ refreshToken: "mantyx_rt_alice" }),
          workspaceSlug: "demo",
          baseUrl: server.baseUrl(),
        }),
    ).toThrow();
  });
});
